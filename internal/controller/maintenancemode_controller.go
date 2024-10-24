package controller

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	ramenv1alpha1 "github.com/ramendr/ramen/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	providerclient "github.com/red-hat-storage/ocs-operator/services/provider/api/v4/client"
	storagev1 "k8s.io/api/storage/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

const (
	maintenanceModeFinalizer = "ocs-client-operator.ocs.openshift.io/maintenance-mode"
	ramenReplicationIdLabel  = "ramendr.openshift.io/replicationID"
)

// MaintenanceModeReconciler reconciles a ClusterVersion object
type MaintenanceModeReconciler struct {
	client.Client
	OperatorNamespace string
	Scheme            *runtime.Scheme

	log             logr.Logger
	ctx             context.Context
	maintenanceMode *ramenv1alpha1.MaintenanceMode
	storageClass    *storagev1.StorageClass
	storageClient   *v1alpha1.StorageClient
}

// SetupWithManager sets up the controller with the Manager.
func (r *MaintenanceModeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	generationChangePredicate := predicate.GenerationChangedPredicate{}
	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&ramenv1alpha1.MaintenanceMode{}, builder.WithPredicates(generationChangePredicate)).
		Watches(&storagev1.StorageClass{}, &handler.EnqueueRequestForObject{}).
		Watches(&v1alpha1.StorageClient{}, &handler.EnqueueRequestForObject{})

	return bldr.Complete(r)
}

//+kubebuilder:rbac:groups=ramendr.openshift.io,resources=maintenancemodes,verbs=get;list;update;create;watch;delete
//+kubebuilder:rbac:groups=ramendr.openshift.io,resources=maintenancemodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ramendr.openshift.io,resources=maintenancemodes/finalizers,verbs=update
//+kubebuilder:rbac:groups=ocs.openshift.io,resources=storageclients,verbs=get;list;watch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

func (r *MaintenanceModeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.ctx = ctx
	r.log = log.FromContext(ctx, "MaintenanceMode", req)
	r.log.Info("Reconciling MaintenanceMode")

	r.maintenanceMode = &ramenv1alpha1.MaintenanceMode{}
	r.maintenanceMode.Name = req.Name
	if err := r.get(r.maintenanceMode); err != nil {
		if kerrors.IsNotFound(err) {
			r.log.Info("Maintenance Mode resource not found. Ignoring since object might be deleted.")
			return reconcile.Result{}, nil
		}
		r.log.Error(err, "failed to get the Maintenance Mode")
		return reconcile.Result{}, err
	}

	err := r.findStorageClassForMaintenanceMode()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to find storageClass for maintenance mode: %v ", err)
	}

	// If no storageClass for the targetID found, exit
	if r.storageClass == nil {
		r.log.Info("no storage class found for the maintenance mode")
		return reconcile.Result{}, nil
	}

	err = r.findStorageClientLinkedWithStorageClass()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to find storageClient for maintenance mode: %v ", err)
	}

	providerClient, err := providerclient.NewProviderClient(
		r.ctx,
		r.storageClient.Spec.StorageProviderEndpoint,
		10*time.Second,
	)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to create provider client with endpoint %v: %v", r.storageClient.Spec.StorageProviderEndpoint, err)
	}
	// Close client-side connections.
	defer providerClient.Close()

	if r.maintenanceMode.GetDeletionTimestamp().IsZero() {

		//ensure finalizer
		if controllerutil.AddFinalizer(r.maintenanceMode, maintenanceModeFinalizer) {
			r.log.Info("finalizer missing on the Maintenance Mode resource, adding...")
			if err := r.Client.Update(r.ctx, r.maintenanceMode); err != nil {
				return ctrl.Result{}, err
			}
		}

		//TODO: call MaintenanceMode Create, GetStatus

	} else {
		// deletion phase
		if err := r.deletionPhase(); err != nil {
			return ctrl.Result{}, err
		}

		//remove finalizer
		if controllerutil.RemoveFinalizer(r.maintenanceMode, maintenanceModeFinalizer) {
			if err := r.Client.Update(r.ctx, r.maintenanceMode); err != nil {
				return ctrl.Result{}, err
			}
			r.log.Info("finallizer removed successfully")
		}
	}
	return ctrl.Result{}, nil
}

func (r *MaintenanceModeReconciler) deletionPhase() error {
	//TODO: call MaintenanceMode Delete, GetStatus
	return nil
}

func (r *MaintenanceModeReconciler) findStorageClassForMaintenanceMode() error {
	storageClassList := &storagev1.StorageClassList{}

	err := r.list(storageClassList)
	if err != nil {
		r.log.Error(err, "unable to list storage classes")
		return err
	}

	for i := range storageClassList.Items {
		storageClass := storageClassList.Items[i]
		if storageClass.GetAnnotations()[ramenReplicationIdLabel] == r.maintenanceMode.Spec.TargetID {
			r.storageClass = &storageClassList.Items[i]
			return err
		}
	}
	return nil
}

func (r *MaintenanceModeReconciler) findStorageClientLinkedWithStorageClass() error {
	r.storageClient = &v1alpha1.StorageClient{}
	val, ok := r.storageClass.GetAnnotations()[storageClientAnnotation]
	if !ok {
		return fmt.Errorf("no storage client linked to storage class %s", r.storageClass.Name)
	}

	r.storageClient.Name = val
	err := r.get(r.storageClient)
	if err != nil {
		return fmt.Errorf("failed to get the storage client: %v", err)
	}
	return nil
}

func (r *MaintenanceModeReconciler) get(obj client.Object, opts ...client.GetOption) error {
	return r.Get(r.ctx, client.ObjectKeyFromObject(obj), obj, opts...)
}

func (r *MaintenanceModeReconciler) list(obj client.ObjectList, opts ...client.ListOption) error {
	return r.List(r.ctx, obj, opts...)
}

func (r *MaintenanceModeReconciler) update(obj client.Object, opts ...client.UpdateOption) error {
	return r.Update(r.ctx, obj, opts...)
}

func (r *MaintenanceModeReconciler) delete(obj client.Object, opts ...client.DeleteOption) error {
	if err := r.Delete(r.ctx, obj, opts...); err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}
