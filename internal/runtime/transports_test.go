package runtime

import (
	"bytes"
	"errors"
	"testing"

	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	localtransport "github.com/dewebprotocol/malt-client/transport/local"
)

func TestComposeCASPolicies(t *testing.T) {
	t.Run("gateway", func(t *testing.T) {
		remote := casmemory.New()
		selected, err := ComposeCAS(&clientconfig.Config{Transport: clientconfig.TransportConfig{
			CASPolicy: clientconfig.CASPolicyGateway,
		}}, remote, true)
		if err != nil || selected.CAS != remote {
			t.Fatalf("gateway selection = %T, %v", selected, err)
		}
		if err := selected.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("local", func(t *testing.T) {
		directory := t.TempDir()
		cfg := &clientconfig.Config{Transport: clientconfig.TransportConfig{
			CASPolicy: clientconfig.CASPolicyLocal, LocalCASDir: directory,
		}}
		if _, err := ComposeCAS(cfg, nil, true); err == nil {
			t.Fatal("local-only CAS satisfied a Gateway-required operation")
		}
		selected, err := ComposeCAS(cfg, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = selected.Close() })
		key, err := selected.Put(t.Context(), []byte("local-only"))
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := localtransport.Open(localtransport.Options{Directory: directory})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		body, err := reopened.Get(t.Context(), key)
		if err != nil || !bytes.Equal(body, []byte("local-only")) {
			t.Fatalf("local-only persisted body = %q, %v", body, err)
		}
	})

	t.Run("hybrid", func(t *testing.T) {
		directory := t.TempDir()
		remote := casmemory.New()
		body := []byte("remote-primary")
		key, err := remote.Put(t.Context(), body)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := ComposeCAS(&clientconfig.Config{Transport: clientconfig.TransportConfig{
			CASPolicy: clientconfig.CASPolicyHybrid, LocalCASDir: directory,
		}}, remote, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = selected.Close() })
		got, err := selected.Get(t.Context(), key)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("hybrid Get = %q, %v", got, err)
		}
		cache, err := localtransport.Open(localtransport.Options{Directory: directory})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cache.Close() })
		cached, err := cache.Get(t.Context(), key)
		if err != nil || !bytes.Equal(cached, body) {
			t.Fatalf("hybrid cache fill = %q, %v", cached, err)
		}
	})
}

func TestCASBindingCloseIsIdempotent(t *testing.T) {
	want := errors.New("close result")
	calls := 0
	binding := &CASBinding{CAS: casmemory.New(), closeFunc: func() error {
		calls++
		if calls == 1 {
			return want
		}
		return nil
	}}
	if err := binding.Close(); !errors.Is(err, want) {
		t.Fatalf("first Close error = %v", err)
	}
	if err := binding.Close(); err != nil || calls != 2 {
		t.Fatalf("second Close error = %v, calls = %d", err, calls)
	}
	if err := binding.Close(); err != nil || calls != 2 {
		t.Fatalf("idempotent Close error = %v, calls = %d", err, calls)
	}
}

func TestComposeCASRejectsInvalidOrMissingCapabilities(t *testing.T) {
	for _, policy := range []string{clientconfig.CASPolicyGateway, clientconfig.CASPolicyHybrid} {
		if _, err := ComposeCAS(&clientconfig.Config{Transport: clientconfig.TransportConfig{
			CASPolicy: policy, LocalCASDir: t.TempDir(),
		}}, nil, false); err == nil {
			t.Fatalf("%s policy accepted a nil Gateway", policy)
		}
	}
	if _, err := ComposeCAS(&clientconfig.Config{Transport: clientconfig.TransportConfig{
		CASPolicy: "peer", LocalCASDir: t.TempDir(),
	}}, casmemory.New(), false); err == nil {
		t.Fatal("unsupported peer policy succeeded")
	}
}
