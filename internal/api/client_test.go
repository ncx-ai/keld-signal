package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceStartAndPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":2}`))
		case "/v1/cli/device/poll":
			w.WriteHeader(202)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	ds, err := c.DeviceStart()
	if err != nil || ds.UserCode != "UC" {
		t.Fatalf("device start %v %v", ds, err)
	}
	res, err := c.DevicePoll("dc")
	if err != nil || res != nil {
		t.Fatalf("202 should give nil,nil; got %v %v", res, err)
	}
}

func TestOnboardingRequiresToken(t *testing.T) {
	c := NewClient("https://x", "")
	if _, err := c.Onboarding(); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestEnrollSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cli/enroll" {
			t.Fatalf("path %s", r.URL.Path)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "AB12-CD34" {
			t.Fatalf("code %q", body["code"])
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"access_token":"tok","principal":"dg@keld.co","org":"Acme"}`))
	}))
	defer srv.Close()
	res, err := NewClient(srv.URL, "").Enroll("AB12-CD34")
	if err != nil {
		t.Fatal(err)
	}
	if res["access_token"] != "tok" || res["org"] != "Acme" {
		t.Fatalf("res %v", res)
	}
}

func TestEnrollExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "").Enroll("x")
	if err == nil {
		t.Fatal("want error on 410")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "invalid") && !strings.Contains(msg, "expired") {
		t.Fatalf("error should mention invalid/expired code, got %q", err.Error())
	}
}

func TestEnrollUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "").Enroll("x")
	if err == nil {
		t.Fatal("want error on 401")
	}
}

func TestAtlasErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	_, err := c.DeviceStart()
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	msg := err.Error()
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
}
