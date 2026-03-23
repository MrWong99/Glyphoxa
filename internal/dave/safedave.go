// Package dave provides a thread-safe wrapper around golibdave.Session.
//
// golibdave's session struct has no internal synchronization, but disgo calls
// it from multiple goroutines concurrently:
//   - The UDP reader goroutine calls Decrypt and MaxDecryptedFrameSize.
//   - The audio sender goroutine calls Encrypt, MaxEncryptedFrameSize,
//     and AssignSsrcToCodec.
//   - The voice gateway goroutine calls AddUser, RemoveUser,
//     OnSelectProtocolAck, and all DAVE transition/epoch methods.
//
// Without a mutex, concurrent map access on session.decryptors causes data
// races (and potential panics). This wrapper serialises all calls through a
// single sync.Mutex.
//
// Additionally, golibdave's passthrough fallback in Decrypt has a reversed
// copy (copy(frame, decryptedFrame) instead of copy(decryptedFrame, frame)).
// The noop session in godave does it correctly. This wrapper does NOT fix that
// bug because the inner session handles it, but it documents the issue.
//
// See: https://github.com/disgoorg/godave (upstream).
package dave

import (
	"log/slog"
	"sync"

	"github.com/disgoorg/godave"
	"github.com/disgoorg/godave/golibdave"
)

// Compile-time interface assertions.
var (
	_ godave.SessionCreateFunc = NewSession
	_ godave.Session           = (*safeSession)(nil)
)

// NewSession is a drop-in replacement for golibdave.NewSession that wraps
// the returned session in a mutex. Pass it to
// voice.WithDaveSessionCreateFunc(dave.NewSession) wherever you would use
// golibdave.NewSession.
func NewSession(logger *slog.Logger, selfUserID godave.UserID, callbacks godave.Callbacks) godave.Session {
	inner := golibdave.NewSession(logger, selfUserID, callbacks)
	return &safeSession{inner: inner}
}

// safeSession wraps a godave.Session with a mutex so that all method calls
// are serialised. This prevents the data race between the UDP reader goroutine
// (which calls Decrypt) and the voice gateway goroutine (which calls AddUser,
// RemoveUser, and the various DAVE transition methods).
type safeSession struct {
	mu    sync.Mutex
	inner godave.Session
}

func (s *safeSession) MaxSupportedProtocolVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.MaxSupportedProtocolVersion()
}

func (s *safeSession) SetChannelID(channelID godave.ChannelID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.SetChannelID(channelID)
}

func (s *safeSession) AssignSsrcToCodec(ssrc uint32, codec godave.Codec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.AssignSsrcToCodec(ssrc, codec)
}

func (s *safeSession) MaxEncryptedFrameSize(frameSize int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.MaxEncryptedFrameSize(frameSize)
}

func (s *safeSession) Encrypt(ssrc uint32, frame []byte, encryptedFrame []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Encrypt(ssrc, frame, encryptedFrame)
}

func (s *safeSession) MaxDecryptedFrameSize(userID godave.UserID, frameSize int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.MaxDecryptedFrameSize(userID, frameSize)
}

func (s *safeSession) Decrypt(userID godave.UserID, frame []byte, decryptedFrame []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Decrypt(userID, frame, decryptedFrame)
}

func (s *safeSession) AddUser(userID godave.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.AddUser(userID)
}

func (s *safeSession) RemoveUser(userID godave.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.RemoveUser(userID)
}

func (s *safeSession) OnSelectProtocolAck(protocolVersion uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnSelectProtocolAck(protocolVersion)
}

func (s *safeSession) OnDavePrepareTransition(transitionID uint16, protocolVersion uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDavePrepareTransition(transitionID, protocolVersion)
}

func (s *safeSession) OnDaveExecuteTransition(transitionID uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDaveExecuteTransition(transitionID)
}

func (s *safeSession) OnDavePrepareEpoch(epoch int, protocolVersion uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDavePrepareEpoch(epoch, protocolVersion)
}

func (s *safeSession) OnDaveMLSExternalSenderPackage(externalSenderPackage []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDaveMLSExternalSenderPackage(externalSenderPackage)
}

func (s *safeSession) OnDaveMLSProposals(proposals []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDaveMLSProposals(proposals)
}

func (s *safeSession) OnDaveMLSPrepareCommitTransition(transitionID uint16, commitMessage []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDaveMLSPrepareCommitTransition(transitionID, commitMessage)
}

func (s *safeSession) OnDaveMLSWelcome(transitionID uint16, welcomeMessage []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner.OnDaveMLSWelcome(transitionID, welcomeMessage)
}
