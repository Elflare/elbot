package cli

import (
	"context"
	"testing"

	"elbot/internal/delivery"
)

func TestRemoteServerTargetClients(t *testing.T) {
	s := &RemoteServer{superadmins: map[string]bool{"local": true}, clients: map[string]map[*remoteClientConn]struct{}{}}
	local := &remoteClientConn{id: "local", server: s}
	guest := &remoteClientConn{id: "guest", server: s}
	s.addClient(local)
	s.addClient(guest)

	clients, err := s.targetClients(context.Background(), delivery.Target{Platform: "cli", Superadmins: true})
	if err != nil {
		t.Fatalf("superadmin target: %v", err)
	}
	if len(clients) != 1 || clients[0].id != "local" {
		t.Fatalf("superadmin clients = %#v", clients)
	}

	clients, err = s.targetClients(context.Background(), delivery.Target{Platform: "cli", PrivateUserID: "guest"})
	if err != nil {
		t.Fatalf("private target: %v", err)
	}
	if len(clients) != 1 || clients[0].id != "guest" {
		t.Fatalf("private clients = %#v", clients)
	}
}

func TestRemoteServerEmptyTargetUsesContextClient(t *testing.T) {
	s := &RemoteServer{clients: map[string]map[*remoteClientConn]struct{}{}}
	client := &remoteClientConn{id: "local", server: s}
	ctx := context.WithValue(context.Background(), remoteClientKey{}, client)
	clients, err := s.targetClients(ctx, delivery.Target{})
	if err != nil {
		t.Fatalf("empty target: %v", err)
	}
	if len(clients) != 1 || clients[0] != client {
		t.Fatalf("clients = %#v", clients)
	}
}
