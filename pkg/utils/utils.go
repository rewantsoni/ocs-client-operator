package utils

import (
	"fmt"
	"strings"
)

func MapKubeletVersionToOCPVersion(version string) (string, error) {
	ocpVersionByKubernetesVersion := map[string]string{
		"1.34": "4.19",
		"1.33": "4.19",
		"1.32": "4.19",
		"1.31": "4.18",
	}

	parts := strings.Split(version, ".")
	majorMinorVersion := strings.Join(parts[:2], ".")
	// Look up the major.minor version in the map
	ocpVersion := ocpVersionByKubernetesVersion[majorMinorVersion]
	if ocpVersion == "" {
		return "", fmt.Errorf("no OCP version found for Kubernetes version %s", majorMinorVersion)
	}

	return ocpVersion, nil
}
