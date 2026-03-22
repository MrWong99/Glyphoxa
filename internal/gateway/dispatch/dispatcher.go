package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// WorkerStarter is a callback invoked after a worker pod is ready. It receives
// the worker's gRPC address (host:port) and should call StartSession on the
// worker. This avoids an import cycle between dispatch and gateway packages.
type WorkerStarter func(ctx context.Context, addr string) error

const (
	// DefaultDispatchTimeout is the maximum time to wait for a worker pod
	// to become ready after Job creation.
	DefaultDispatchTimeout = 120 * time.Second

	// DefaultGRPCPort is the default gRPC port on worker pods.
	DefaultGRPCPort = int32(50051)

	podLogTailLines     = int64(50)
	defaultPollInterval = 2 * time.Second
)

// DispatchResult contains the result of a successful dispatch.
type DispatchResult struct {
	JobName string
	PodName string
	PodIP   string
	Address string // podIP:grpcPort
}

// Dispatcher creates Kubernetes Jobs for voice sessions and manages their
// full lifecycle: creation, pod readiness detection, and cleanup.
type Dispatcher struct {
	k8s          kubernetes.Interface
	namespace    string
	jobTemplate  *batchv1.Job
	grpcPort     int32
	timeout      time.Duration
	pollInterval time.Duration
	log          *slog.Logger

	mu       sync.Mutex
	sessions map[string]*dispatchedSession
}

type dispatchedSession struct {
	jobName string
	podName string
	cancel  context.CancelFunc
}

// NewDispatcher creates a new Dispatcher.
func NewDispatcher(k8s kubernetes.Interface, namespace string, jobTemplate *batchv1.Job, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		k8s:          k8s,
		namespace:    namespace,
		jobTemplate:  jobTemplate,
		grpcPort:     DefaultGRPCPort,
		timeout:      DefaultDispatchTimeout,
		pollInterval: defaultPollInterval,
		log:          slog.Default(),
		sessions:     make(map[string]*dispatchedSession),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithGRPCPort sets the gRPC port used to construct worker addresses.
func WithGRPCPort(port int32) Option {
	return func(d *Dispatcher) { d.grpcPort = port }
}

// WithTimeout sets the maximum time to wait for pod readiness.
func WithTimeout(t time.Duration) Option {
	return func(d *Dispatcher) { d.timeout = t }
}

// WithPollInterval sets the pod status polling interval.
func WithPollInterval(interval time.Duration) Option {
	return func(d *Dispatcher) { d.pollInterval = interval }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(d *Dispatcher) { d.log = l }
}

// Dispatch creates a K8s Job for the given session and blocks until the
// worker pod is ready. It returns the pod's gRPC address on success.
// On any failure the Job is cleaned up automatically.
func (d *Dispatcher) Dispatch(ctx context.Context, sessionID, tenantID string, starter WorkerStarter) (*DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)

	job := StampJob(d.jobTemplate, sessionID, tenantID)

	created, err := d.k8s.BatchV1().Jobs(d.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		cancel()
		if errors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("dispatch: job already exists for session %s", sessionID)
		}
		return nil, fmt.Errorf("dispatch: create job: %w", err)
	}

	d.mu.Lock()
	d.sessions[sessionID] = &dispatchedSession{
		jobName: created.Name,
		cancel:  cancel,
	}
	d.mu.Unlock()

	podName, podIP, err := d.waitForPod(ctx, created.Name)
	if err != nil {
		d.capturePodLogs(context.Background(), created.Name)
		_ = d.deleteJob(context.Background(), created.Name)
		d.removeSession(sessionID)
		cancel()
		return nil, err
	}

	addr := fmt.Sprintf("%s:%d", podIP, d.grpcPort)

	d.mu.Lock()
	if s, ok := d.sessions[sessionID]; ok {
		s.podName = podName
	}
	d.mu.Unlock()

	// Call StartSession on the worker via the callback.
	if starter != nil {
		if callErr := starter(ctx, addr); callErr != nil {
			d.capturePodLogs(context.Background(), created.Name)
			_ = d.deleteJob(context.Background(), created.Name)
			d.removeSession(sessionID)
			cancel()
			return nil, fmt.Errorf("dispatch: start session on worker: %w", callErr)
		}
		slog.Info("dispatch: worker session started",
			"session_id", sessionID,
			"address", addr,
		)
	}
	return &DispatchResult{
		JobName: created.Name,
		PodName: podName,
		PodIP:   podIP,
		Address: addr,
	}, nil
}

// Stop deletes the K8s Job for the given session.
func (d *Dispatcher) Stop(ctx context.Context, sessionID string) error {
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("dispatch: session %s not found", sessionID)
	}
	delete(d.sessions, sessionID)
	d.mu.Unlock()

	sess.cancel()
	return d.deleteJob(ctx, sess.jobName)
}

// Cleanup stops all active sessions and deletes their Jobs.
// Intended for graceful shutdown.
func (d *Dispatcher) Cleanup(ctx context.Context) error {
	d.mu.Lock()
	sessions := make(map[string]*dispatchedSession, len(d.sessions))
	for k, v := range d.sessions {
		sessions[k] = v
	}
	d.sessions = make(map[string]*dispatchedSession)
	d.mu.Unlock()

	var firstErr error
	for sid, sess := range sessions {
		sess.cancel()
		if err := d.deleteJob(ctx, sess.jobName); err != nil {
			d.log.Error("dispatch: cleanup failed", "session_id", sid, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// CleanupOrphanedJobs lists all Jobs with the glyphoxa.io/session-id label
// and deletes any whose session ID is not in the provided active set.
// This handles orphaned Jobs left behind after a gateway restart.
func (d *Dispatcher) CleanupOrphanedJobs(ctx context.Context, activeSessionIDs map[string]struct{}) error {
	jobs, err := d.k8s.BatchV1().Jobs(d.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("dispatch: list jobs: %w", err)
	}

	var firstErr error
	for i := range jobs.Items {
		job := &jobs.Items[i]
		sid, ok := job.Labels["glyphoxa.io/session-id"]
		if !ok {
			continue
		}
		if _, active := activeSessionIDs[sid]; active {
			continue
		}
		d.log.Info("dispatch: deleting orphaned job", "job", job.Name, "session_id", sid)
		if err := d.deleteJob(ctx, job.Name); err != nil {
			d.log.Error("dispatch: delete orphaned job failed", "job", job.Name, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// waitForPod polls for the Job's pod to reach Running phase with a started
// container. Returns the pod name and IP on success.
func (d *Dispatcher) waitForPod(ctx context.Context, jobName string) (podName, podIP string, err error) {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("dispatch: pod readiness timeout for job %s: %w", jobName, ctx.Err())
		case <-ticker.C:
			pods, listErr := d.k8s.CoreV1().Pods(d.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("job-name=%s", jobName),
			})
			if listErr != nil {
				d.log.Warn("dispatch: list pods", "job", jobName, "error", listErr)
				continue
			}

			for i := range pods.Items {
				pod := &pods.Items[i]
				// Client-side label filter for fake client compatibility.
				if pod.Labels["job-name"] != jobName {
					continue
				}

				if pod.Status.Phase == corev1.PodFailed {
					return "", "", fmt.Errorf("dispatch: pod %s failed", pod.Name)
				}

				if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP != "" {
					for _, cs := range pod.Status.ContainerStatuses {
						if cs.Started != nil && *cs.Started {
							return pod.Name, pod.Status.PodIP, nil
						}
					}
				}
			}
		}
	}
}

// capturePodLogs attempts to log the last 50 lines of the worker pod's
// output for debugging startup failures. Best-effort; errors are silenced.
func (d *Dispatcher) capturePodLogs(ctx context.Context, jobName string) {
	pods, err := d.k8s.CoreV1().Pods(d.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil || len(pods.Items) == 0 {
		return
	}

	tailLines := podLogTailLines
	req := d.k8s.CoreV1().Pods(d.namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{
		TailLines: &tailLines,
	})
	raw, err := req.Do(ctx).Raw()
	if err != nil {
		return
	}

	d.log.Error("dispatch: worker pod logs on failure",
		"pod", pods.Items[0].Name,
		"job", jobName,
		"logs", string(raw),
	)
}

func (d *Dispatcher) deleteJob(ctx context.Context, jobName string) error {
	propagation := metav1.DeletePropagationBackground
	err := d.k8s.BatchV1().Jobs(d.namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("dispatch: delete job %s: %w", jobName, err)
	}
	return nil
}

func (d *Dispatcher) removeSession(sessionID string) {
	d.mu.Lock()
	if sess, ok := d.sessions[sessionID]; ok {
		sess.cancel()
		delete(d.sessions, sessionID)
	}
	d.mu.Unlock()
}
