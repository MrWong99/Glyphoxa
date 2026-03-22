package dispatch

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func boolPtr(b bool) *bool { return &b }

func readyPod(namespace, podName, jobName, podIP string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{"job-name": jobName},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: podIP,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:    "worker",
				Started: boolPtr(true),
			}},
		},
	}
}

func failedPod(namespace, podName, jobName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels:    map[string]string{"job-name": jobName},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
		},
	}
}

func TestDispatch_HappyPath(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()
	sessionID := "550e8400-e29b-41d4-a716-446655440000"
	expectedJobName := "glyphoxa-worker-550e8400"

	var podReady atomic.Bool
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if !podReady.Load() {
			return true, &corev1.PodList{}, nil
		}
		return true, &corev1.PodList{
			Items: []corev1.Pod{*readyPod(ns, "worker-pod-1", expectedJobName, "10.0.0.42")},
		}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	go func() {
		time.Sleep(50 * time.Millisecond)
		podReady.Store(true)
	}()

	result, err := d.Dispatch(context.Background(), sessionID, "tenant-1", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if result.JobName != expectedJobName {
		t.Errorf("JobName = %q, want %q", result.JobName, expectedJobName)
	}
	if result.PodName != "worker-pod-1" {
		t.Errorf("PodName = %q, want %q", result.PodName, "worker-pod-1")
	}
	if result.PodIP != "10.0.0.42" {
		t.Errorf("PodIP = %q, want %q", result.PodIP, "10.0.0.42")
	}
	if result.Address != "10.0.0.42:50051" {
		t.Errorf("Address = %q, want %q", result.Address, "10.0.0.42:50051")
	}

	// Job should exist in the fake client.
	job, err := client.BatchV1().Jobs(ns).Get(context.Background(), expectedJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job not found: %v", err)
	}
	if got := job.Labels["glyphoxa.io/session-id"]; got != sessionID {
		t.Errorf("job label session-id = %q, want %q", got, sessionID)
	}
}

func TestDispatch_JobAlreadyExists(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()
	sessionID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	// Pre-create the job so it already exists.
	job := StampJob(template, sessionID, "t1")
	job.Namespace = ns
	_, err := client.BatchV1().Jobs(ns).Create(context.Background(), job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	d := NewDispatcher(client, ns, template, WithTimeout(time.Second))
	_, err = d.Dispatch(context.Background(), sessionID, "t1", nil)
	if err == nil {
		t.Fatal("expected error for duplicate job")
	}
	if !containsStr(err.Error(), "already exists") {
		t.Errorf("error %q should mention 'already exists'", err.Error())
	}
}

func TestDispatch_PodTimeout(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()

	// Pods never become ready.
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(100*time.Millisecond),
	)

	_, err := d.Dispatch(context.Background(), "timeout-sess", "t1", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !containsStr(err.Error(), "timeout") {
		t.Errorf("error %q should mention 'timeout'", err.Error())
	}

	// Job should have been cleaned up.
	jobs, _ := client.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected job to be deleted, got %d jobs", len(jobs.Items))
	}
}

func TestDispatch_PodFailed(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()
	expectedJobName := "glyphoxa-worker-failsess"

	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{
			Items: []corev1.Pod{*failedPod(ns, "failed-pod", expectedJobName)},
		}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	_, err := d.Dispatch(context.Background(), "failsess", "t1", nil)
	if err == nil {
		t.Fatal("expected error for failed pod")
	}
	if !containsStr(err.Error(), "failed") {
		t.Errorf("error %q should mention 'failed'", err.Error())
	}
}

func TestStop(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()
	sessionID := "stop-test-1234-5678-abcdefabcdef"
	expectedJobName := "glyphoxa-worker-stoptest"

	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{
			Items: []corev1.Pod{*readyPod(ns, "pod-1", expectedJobName, "10.0.0.1")},
		}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	_, err := d.Dispatch(context.Background(), sessionID, "t1", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	err = d.Stop(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Job should be deleted.
	jobs, _ := client.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected job deleted, got %d", len(jobs.Items))
	}
}

func TestStop_UnknownSession(t *testing.T) {
	t.Parallel()

	d := NewDispatcher(fake.NewSimpleClientset(), "default", makeTestJobTemplate())
	err := d.Stop(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
	if !containsStr(err.Error(), "not found") {
		t.Errorf("error %q should mention 'not found'", err.Error())
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()

	// Known job names for our session IDs.
	// "cleanup-a1234567" → sanitize → "cleanupa" → job name "glyphoxa-worker-cleanupa"
	// "cleanup-b1234567" → sanitize → "cleanupb" → job name "glyphoxa-worker-cleanupb"
	knownJobs := map[string]string{
		"glyphoxa-worker-cleanupa": "10.0.0.1",
		"glyphoxa-worker-cleanupb": "10.0.0.2",
	}

	// Return ready pods for known job names without calling back into the
	// fake client (which would deadlock on its internal mutex).
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var pods []corev1.Pod
		for jobName, ip := range knownJobs {
			pods = append(pods, *readyPod(ns, "pod-"+jobName, jobName, ip))
		}
		return true, &corev1.PodList{Items: pods}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	for _, sid := range []string{"cleanup-a1234567", "cleanup-b1234567"} {
		if _, err := d.Dispatch(context.Background(), sid, "t1", nil); err != nil {
			t.Fatalf("Dispatch(%s): %v", sid, err)
		}
	}

	err := d.Cleanup(context.Background())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	jobs, _ := client.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected all jobs deleted, got %d", len(jobs.Items))
	}
}

func TestCleanupOrphanedJobs(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()

	// Create three jobs: two orphaned, one active.
	for _, tc := range []struct {
		name, sid string
	}{
		{"orphan-job-1", "orphan-session-1"},
		{"orphan-job-2", "orphan-session-2"},
		{"active-job-1", "active-session-1"},
	} {
		job := template.DeepCopy()
		job.Name = tc.name
		job.Namespace = ns
		job.Labels = map[string]string{"glyphoxa.io/session-id": tc.sid}
		if _, err := client.BatchV1().Jobs(ns).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	// Also create a job without the glyphoxa label (should be ignored).
	unrelated := template.DeepCopy()
	unrelated.Name = "unrelated-job"
	unrelated.Namespace = ns
	if _, err := client.BatchV1().Jobs(ns).Create(context.Background(), unrelated, metav1.CreateOptions{}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	d := NewDispatcher(client, ns, template)

	activeSet := map[string]struct{}{
		"active-session-1": {},
	}
	err := d.CleanupOrphanedJobs(context.Background(), activeSet)
	if err != nil {
		t.Fatalf("CleanupOrphanedJobs: %v", err)
	}

	jobs, _ := client.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{})
	remaining := make(map[string]bool)
	for _, j := range jobs.Items {
		remaining[j.Name] = true
	}
	if !remaining["active-job-1"] {
		t.Error("active-job-1 should not have been deleted")
	}
	if !remaining["unrelated-job"] {
		t.Error("unrelated-job should not have been deleted")
	}
	if remaining["orphan-job-1"] {
		t.Error("orphan-job-1 should have been deleted")
	}
	if remaining["orphan-job-2"] {
		t.Error("orphan-job-2 should have been deleted")
	}
}

func TestDispatch_ConcurrentSessions(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()

	// Build all expected job names upfront so the reactor can return
	// matching pods without calling back into the fake client.
	const n = 10
	type sessionInfo struct {
		sid     string
		jobName string
	}
	sessions := make([]sessionInfo, n)
	for i := range n {
		// Use format that produces unique 8-char sanitized names.
		sid := fmt.Sprintf("%08x-session", i)
		sessions[i] = sessionInfo{
			sid:     sid,
			jobName: "glyphoxa-worker-" + sanitizeForDNS(sid),
		}
	}

	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		var pods []corev1.Pod
		for _, s := range sessions {
			pods = append(pods, *readyPod(ns, "pod-"+s.jobName, s.jobName, "10.0.0.1"))
		}
		return true, &corev1.PodList{Items: pods}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = d.Dispatch(context.Background(), sessions[idx].sid, "t1", nil)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("session %d (%s): %v", i, sessions[i].sid, err)
		}
	}

	jobs, _ := client.BatchV1().Jobs(ns).List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != n {
		t.Errorf("expected %d jobs, got %d", n, len(jobs.Items))
	}

	// All job names should be unique.
	names := make(map[string]bool)
	for _, j := range jobs.Items {
		if names[j.Name] {
			t.Errorf("duplicate job name: %s", j.Name)
		}
		names[j.Name] = true
	}
}

func TestDispatch_ContextCancelled(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()

	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := d.Dispatch(ctx, "cancel-test-1234", "t1", nil)
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
}

func TestDispatch_CustomGRPCPort(t *testing.T) {
	t.Parallel()

	client := fake.NewSimpleClientset()
	ns := "default"
	template := makeTestJobTemplate()
	expectedJobName := "glyphoxa-worker-custompo"

	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{
			Items: []corev1.Pod{*readyPod(ns, "pod-1", expectedJobName, "10.0.0.5")},
		}, nil
	})

	d := NewDispatcher(client, ns, template,
		WithPollInterval(10*time.Millisecond),
		WithTimeout(5*time.Second),
		WithGRPCPort(9090),
	)

	result, err := d.Dispatch(context.Background(), "customport-1234", "t1", nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.Address != "10.0.0.5:9090" {
		t.Errorf("Address = %q, want %q", result.Address, "10.0.0.5:9090")
	}
}
