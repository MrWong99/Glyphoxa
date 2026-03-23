package dave

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/disgoorg/godave"
)

// stubSession is a minimal godave.Session that records method calls.
type stubSession struct {
	mu             sync.Mutex
	addUserCalls   int
	decryptCalls   int
	encryptCalls   int
	removeCalls    int
	decryptorUsers map[godave.UserID]bool
}

func newStubSession() *stubSession {
	return &stubSession{
		decryptorUsers: make(map[godave.UserID]bool),
	}
}

func (s *stubSession) MaxSupportedProtocolVersion() int                  { return 1 }
func (s *stubSession) SetChannelID(_ godave.ChannelID)                   {}
func (s *stubSession) AssignSsrcToCodec(_ uint32, _ godave.Codec)        {}
func (s *stubSession) MaxEncryptedFrameSize(frameSize int) int           { return frameSize }
func (s *stubSession) MaxDecryptedFrameSize(_ godave.UserID, fs int) int { return fs }
func (s *stubSession) OnSelectProtocolAck(_ uint16)                      {}
func (s *stubSession) OnDavePrepareTransition(_ uint16, _ uint16)        {}
func (s *stubSession) OnDaveExecuteTransition(_ uint16)                  {}
func (s *stubSession) OnDavePrepareEpoch(_ int, _ uint16)                {}
func (s *stubSession) OnDaveMLSExternalSenderPackage(_ []byte)           {}
func (s *stubSession) OnDaveMLSProposals(_ []byte)                       {}
func (s *stubSession) OnDaveMLSPrepareCommitTransition(_ uint16, _ []byte) {
}
func (s *stubSession) OnDaveMLSWelcome(_ uint16, _ []byte) {}

func (s *stubSession) Encrypt(_ uint32, frame []byte, encryptedFrame []byte) (int, error) {
	s.mu.Lock()
	s.encryptCalls++
	s.mu.Unlock()
	return copy(encryptedFrame, frame), nil
}

func (s *stubSession) Decrypt(_ godave.UserID, frame []byte, decryptedFrame []byte) (int, error) {
	s.mu.Lock()
	s.decryptCalls++
	s.mu.Unlock()
	return copy(decryptedFrame, frame), nil
}

func (s *stubSession) AddUser(userID godave.UserID) {
	s.mu.Lock()
	s.addUserCalls++
	s.decryptorUsers[userID] = true
	s.mu.Unlock()
}

func (s *stubSession) RemoveUser(userID godave.UserID) {
	s.mu.Lock()
	s.removeCalls++
	delete(s.decryptorUsers, userID)
	s.mu.Unlock()
}

func TestSafeSession_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ godave.Session = (*safeSession)(nil)
}

func TestSafeSession_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	stub := newStubSession()
	ss := &safeSession{inner: stub}

	const goroutines = 50
	const opsPerGoroutine = 100

	var wg sync.WaitGroup

	// Simulate the UDP reader goroutine calling Decrypt concurrently
	// with the gateway goroutine calling AddUser/RemoveUser.
	wg.Add(goroutines * 3)

	// Decrypt goroutines (simulating UDP reader).
	for range goroutines {
		go func() {
			defer wg.Done()
			frame := []byte{0x01, 0x02, 0x03}
			out := make([]byte, 3)
			for range opsPerGoroutine {
				_, _ = ss.Decrypt("user-1", frame, out)
			}
		}()
	}

	// AddUser goroutines (simulating gateway).
	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range opsPerGoroutine {
				uid := godave.UserID(string(rune('A' + i%26)))
				ss.AddUser(uid)
			}
		}()
	}

	// Encrypt goroutines (simulating audio sender).
	for range goroutines {
		go func() {
			defer wg.Done()
			frame := []byte{0x04, 0x05}
			out := make([]byte, 2)
			for range opsPerGoroutine {
				_, _ = ss.Encrypt(12345, frame, out)
			}
		}()
	}

	wg.Wait()

	// Verify all operations completed.
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if stub.decryptCalls != goroutines*opsPerGoroutine {
		t.Errorf("expected %d decrypt calls, got %d", goroutines*opsPerGoroutine, stub.decryptCalls)
	}
	if stub.encryptCalls != goroutines*opsPerGoroutine {
		t.Errorf("expected %d encrypt calls, got %d", goroutines*opsPerGoroutine, stub.encryptCalls)
	}
	if stub.addUserCalls != goroutines*opsPerGoroutine {
		t.Errorf("expected %d addUser calls, got %d", goroutines*opsPerGoroutine, stub.addUserCalls)
	}
}

func TestNewSession_ReturnsSessionInterface(t *testing.T) {
	t.Parallel()

	// NewSession should return a godave.Session. We verify the create func
	// signature matches godave.SessionCreateFunc at compile time (see var block
	// in safedave.go). Here we also verify at runtime that the returned value
	// satisfies the interface. We cannot call golibdave.NewSession in unit tests
	// without the C library, so we only check the type assertion on a manually
	// constructed safeSession.
	var s godave.Session = &safeSession{inner: newStubSession()}
	if s == nil {
		t.Fatal("safeSession should satisfy godave.Session")
	}
}

func TestNewSession_CreateFuncSignature(t *testing.T) {
	t.Parallel()

	// Verify the function signature is compatible.
	var fn godave.SessionCreateFunc = func(logger *slog.Logger, userID godave.UserID, callbacks godave.Callbacks) godave.Session {
		stub := newStubSession()
		return &safeSession{inner: stub}
	}
	session := fn(slog.Default(), "test-user", nil)
	if session == nil {
		t.Fatal("create func should return non-nil session")
	}
}
