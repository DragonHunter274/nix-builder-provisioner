package gradientproto

import "testing"

func TestDecodeServerMessage(t *testing.T) {
	t.Run("AuthChallenge empty", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_AuthChallenge_empty"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgAuthChallenge || len(msg.Peers) != 0 {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("AuthChallenge peers", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_AuthChallenge_peers"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgAuthChallenge {
			t.Fatalf("Kind = %v", msg.Kind)
		}
		if len(msg.Peers) != 2 || msg.Peers[0] != "peer-1" || msg.Peers[1] != "peer-2" {
			t.Errorf("Peers = %v", msg.Peers)
		}
	})

	t.Run("InitAck", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_InitAck"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgInitAck {
			t.Fatalf("Kind = %v", msg.Kind)
		}
		if msg.Version != 1 {
			t.Errorf("Version = %d, want 1", msg.Version)
		}
		if !msg.Capabilities.Core || !msg.Capabilities.Build || !msg.Capabilities.Cache {
			t.Errorf("Capabilities = %+v", msg.Capabilities)
		}
		if len(msg.AuthorizedPeers) != 1 || msg.AuthorizedPeers[0] != "peer-1" {
			t.Errorf("AuthorizedPeers = %v", msg.AuthorizedPeers)
		}
		if len(msg.FailedPeers) != 1 || msg.FailedPeers[0].PeerID != "peer-2" || msg.FailedPeers[0].Reason != "bad token" {
			t.Errorf("FailedPeers = %v", msg.FailedPeers)
		}
	})

	t.Run("Reject", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_Reject"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgReject || msg.Code != 401 || msg.Reason != "unauthorized" {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("Error", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_Error"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgError || msg.Code != 500 || msg.Message != "internal error" {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("Draining", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_Draining"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgDraining {
			t.Errorf("Kind = %v", msg.Kind)
		}
	})

	t.Run("AssignJob Build", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_AssignJob_Build"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgAssignJob || msg.JobID != "j-1" {
			t.Fatalf("got Kind=%v JobID=%q", msg.Kind, msg.JobID)
		}
		if len(msg.Job.Builds) != 3 {
			t.Fatalf("Job.Builds = %d, want 3", len(msg.Job.Builds))
		}
		if msg.Job.Builds[1].BuildID != "b-0000000000000000000000000000000000000001" {
			t.Errorf("Job.Builds[1].BuildID = %q", msg.Job.Builds[1].BuildID)
		}
	})

	t.Run("AbortJob", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_AbortJob"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgAbortJob || msg.JobID != "j-1" || msg.Reason != "cancelled by user" {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("NarPushResume", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_NarPushResume"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgNarPushResume || msg.JobID != "j-1" ||
			msg.StorePath != "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-hello-2.12" ||
			msg.ReceivedBytes != 2048 {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("CacheStatus empty", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_CacheStatus_empty"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgCacheStatus || msg.QueryID != "q-1" || len(msg.Cached) != 0 {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("CacheStatus populated", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_CacheStatus_populated"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgCacheStatus {
			t.Fatalf("Kind = %v", msg.Kind)
		}
		if len(msg.Cached) != 1 {
			t.Fatalf("Cached len = %d, want 1", len(msg.Cached))
		}
		cp := msg.Cached[0]
		if cp.Path != "/nix/store/dddddddddddddddddddddddddddddddd-foo" || !cp.Cached ||
			cp.FileSize == nil || *cp.FileSize != 1000 ||
			cp.URL == nil || *cp.URL != "https://s3.example.com/presigned?sig=abc" {
			t.Errorf("Cached[0] = %+v", cp)
		}
	})

	t.Run("CacheError", func(t *testing.T) {
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_CacheError"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgCacheError || msg.QueryID != "q-1" || msg.Message != "db timeout" {
			t.Errorf("got %+v", msg)
		}
	})

	t.Run("unknown variant does not error", func(t *testing.T) {
		// RequestAllScores (tag 20, unit variant) is intentionally not
		// implemented - verify it decodes to ServerMsgUnknown instead of
		// erroring.
		msg, err := DecodeServerMessage(loadFixture(t, "ServerMessage_RequestAllScores"))
		if err != nil {
			t.Fatal(err)
		}
		if msg.Kind != ServerMsgUnknown || msg.UnknownTag != 20 {
			t.Errorf("got Kind=%v UnknownTag=%d, want Unknown/20", msg.Kind, msg.UnknownTag)
		}
	})
}
