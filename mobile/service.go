package mobile

import (
	"context"
	"encoding/base64"

	"github.com/laincat/veneracat-core/service"
)

// Service packages for a foreign caller.
//
// A service is a zip holding service.json and the code it names. Installing,
// listing and removing the package is Core's business; account values, the
// network and the interface belong to the client. Everything here takes and
// returns JSON text because gomobile only exports basic types.
//
// Running a service is deliberately absent: it needs the client's transport
// and its account values, so a client that wires it also owns the Host it runs
// with.

// ServiceInstall verifies and installs a service package into dataDir.
//
// archiveBase64 is the package bytes, base64 as usual for binary through a JSON
// shaped API. The result is the install record as JSON.
func ServiceInstall(dataDir, origin, digest, archiveBase64 string) (_ string, err error) {
	defer guard(&err)
	raw, decodeErr := base64.StdEncoding.DecodeString(archiveBase64)
	if decodeErr != nil {
		return "", decodeErr
	}
	rec, installErr := service.InstallPackage(context.Background(), dataDir, origin, digest, raw)
	if installErr != nil {
		return "", installErr
	}
	return encode(rec)
}

// ServiceList returns the installed services as a JSON array.
func ServiceList(dataDir string) (_ string, err error) {
	defer guard(&err)
	rows, listErr := service.InstalledPackages(dataDir)
	if listErr != nil {
		return "", listErr
	}
	if rows == nil {
		return "[]", nil
	}
	return encode(rows)
}

// ServiceRemove deletes an installed service package.
func ServiceRemove(dataDir, id string) (err error) {
	defer guard(&err)
	return service.RemovePackage(dataDir, id)
}

// ServiceManifest returns the install record of one service as JSON, so a
// client can show what it asks for without unpacking it again.
func ServiceManifest(dataDir, id string) (_ string, err error) {
	defer guard(&err)
	rec, readErr := service.InstalledPackage(dataDir, id)
	if readErr != nil {
		return "", readErr
	}
	return encode(rec)
}

// ServiceSource returns the entry code of an installed service. A client that
// runs it needs the code; one that only lists it does not.
func ServiceSource(dataDir, id string) (_ string, err error) {
	defer guard(&err)
	_, source, readErr := service.ReadInstalledPackage(dataDir, id)
	if readErr != nil {
		return "", readErr
	}
	return source, nil
}
