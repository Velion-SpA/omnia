package auth

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDeviceToucher records TouchDevice calls so tests can assert the authorize
// path stamps last_seen_at for device-bound tokens.
type fakeDeviceToucher struct{ touched []string }

func (f *fakeDeviceToucher) TouchDevice(_ context.Context, id string) error {
	f.touched = append(f.touched, id)
	return nil
}

func newTouchTestService(t *testing.T, dt deviceToucher) *Service {
	t.Helper()
	svc, err := NewService(nil, strings.Repeat("k", 32))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.deviceToucher = dt
	return svc
}

// TestAuthorizeAccountTouchesDeviceBoundToken verifies a device-bound account
// token stamps the device's last_seen on authorize (OBL-08).
func TestAuthorizeAccountTouchesDeviceBoundToken(t *testing.T) {
	dt := &fakeDeviceToucher{}
	svc := newTouchTestService(t, dt)

	token, err := svc.MintAccountTokenForDevice("acc-1", "alice", "dev-9")
	if err != nil {
		t.Fatalf("MintAccountTokenForDevice: %v", err)
	}
	req := httptest.NewRequest("GET", "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	claims, err := svc.AuthorizeAccount(req)
	if err != nil {
		t.Fatalf("AuthorizeAccount: %v", err)
	}
	if claims == nil || claims.DeviceID != "dev-9" {
		t.Fatalf("expected device-bound claims, got %+v", claims)
	}
	if len(dt.touched) != 1 || dt.touched[0] != "dev-9" {
		t.Fatalf("expected TouchDevice(dev-9) once, got %v", dt.touched)
	}
}

// TestAuthorizeAccountPlainTokenDoesNotTouch verifies a token without a DeviceID
// never touches any device.
func TestAuthorizeAccountPlainTokenDoesNotTouch(t *testing.T) {
	dt := &fakeDeviceToucher{}
	svc := newTouchTestService(t, dt)

	token, err := svc.MintAccountToken("acc-1", "alice")
	if err != nil {
		t.Fatalf("MintAccountToken: %v", err)
	}
	req := httptest.NewRequest("GET", "/sync/pull", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	if _, err := svc.AuthorizeAccount(req); err != nil {
		t.Fatalf("AuthorizeAccount: %v", err)
	}
	if len(dt.touched) != 0 {
		t.Fatalf("plain token must not touch any device, got %v", dt.touched)
	}
}
