package cloudserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/velion/omnia/internal/cloud/auth"
	"github.com/velion/omnia/internal/cloud/cloudstore"
)

// deviceManager is the subset of cloudstore needed for device-management endpoints.
type deviceManager interface {
	ListDevicesForAccount(accountID string) ([]cloudstore.Device, error)
	GetDevice(id string) (*cloudstore.Device, error)
	SetDeviceScope(id string, projects []string) error
	DeleteDevice(id string) error
}

// Compile-time assertion: *cloudstore.CloudStore must satisfy deviceManager.
var _ deviceManager = (*cloudstore.CloudStore)(nil)

// deviceView is the JSON shape for a device in responses.
type deviceView struct {
	ID            string   `json:"id"`
	AccountID     string   `json:"account_id"`
	Name          string   `json:"name"`
	ScopeProjects []string `json:"scope_projects"`
}

func toDeviceView(d cloudstore.Device) deviceView {
	scope := d.ScopeProjects
	if scope == nil {
		scope = []string{}
	}
	return deviceView{ID: d.ID, AccountID: d.AccountID, Name: d.Name, ScopeProjects: scope}
}

// deviceMgr returns the store typed as a deviceManager, or (nil,false).
func (s *CloudServer) deviceMgr() (deviceManager, bool) {
	dm, ok := s.store.(deviceManager)
	return dm, ok
}

// requireDeviceManagement is the common gate for device-management endpoints.
// It requires an authenticated account (claims != nil). Returns (dm, claims, ok).
func (s *CloudServer) requireDeviceManagement(w http.ResponseWriter, r *http.Request) (deviceManager, *auth.AccountClaims, bool) {
	dm, ok := s.deviceMgr()
	if !ok {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "device management unavailable"})
		return nil, nil, false
	}
	claims, _ := auth.AccountFromContext(r.Context())
	if claims == nil {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "account authentication required"})
		return nil, nil, false
	}
	return dm, claims, true
}

// handleListDevices handles GET /devices.
// Returns all devices registered to the caller's account.
func (s *CloudServer) handleListDevices(w http.ResponseWriter, r *http.Request) {
	dm, claims, ok := s.requireDeviceManagement(w, r)
	if !ok {
		return
	}
	devices, err := dm.ListDevicesForAccount(claims.AccountID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not list devices"})
		return
	}
	out := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		out = append(out, toDeviceView(d))
	}
	jsonResponse(w, http.StatusOK, out)
}

type setScopeRequest struct {
	Projects []string `json:"projects"`
}

// handleSetDeviceScope handles POST /devices/{id}/scope.
// The caller must own the device. Ownership: device.AccountID == claims.AccountID.
func (s *CloudServer) handleSetDeviceScope(w http.ResponseWriter, r *http.Request) {
	dm, claims, ok := s.requireDeviceManagement(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "device id is required"})
		return
	}
	dev, err := dm.GetDevice(id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not load device"})
		return
	}
	// Return 404 (not 403) to avoid leaking existence of devices owned by other accounts.
	if dev == nil || dev.AccountID != claims.AccountID {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	var req setScopeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := dm.SetDeviceScope(id, req.Projects); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not set device scope"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDeleteDevice handles DELETE /devices/{id}.
// The caller must own the device. Returns 404 for unknown or unowned devices.
func (s *CloudServer) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	dm, claims, ok := s.requireDeviceManagement(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "device id is required"})
		return
	}
	dev, err := dm.GetDevice(id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not load device"})
		return
	}
	// Return 404 for missing or unowned devices — do not leak existence.
	if dev == nil || dev.AccountID != claims.AccountID {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	if err := dm.DeleteDevice(id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "could not delete device"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
