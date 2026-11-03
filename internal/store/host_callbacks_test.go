package store

import (
	"agent-core/internal/model"
	"testing"
)

func TestHostCallbackRoundTrip(t *testing.T) {
	store := newTestStore(t)
	callback := &model.HostCallback{Name: "host", Description: "test", Schema: []byte(`{"type":"object"}`),
		CallbackURL: "http://127.0.0.1/callback", Secret: "secret", AllowPrivate: true, Enabled: true}
	if err := store.SaveHostCallback(callback); err != nil {
		t.Fatal(err)
	}
	callbacks, err := store.HostCallbacks()
	if err != nil || len(callbacks) != 1 {
		t.Fatalf("callbacks=%#v err=%v", callbacks, err)
	}
	if !callbacks[0].AllowPrivate || callbacks[0].Secret != "secret" || string(callbacks[0].Schema) != `{"type":"object"}` {
		t.Fatalf("unexpected callback: %#v", callbacks[0])
	}
	if err := store.DeleteHostCallback("host"); err != nil {
		t.Fatal(err)
	}
}
