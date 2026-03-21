package dispatch

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const validJobYAML = `apiVersion: batch/v1
kind: Job
metadata:
  name: glyphoxa-worker-SESSION_ID
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: worker
        image: glyphoxa/worker:latest
        ports:
        - name: grpc
          containerPort: 50051
`

func TestLoadJobTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configMaps []*corev1.ConfigMap
		cmName     string
		wantErr    string
	}{
		{
			name: "valid configmap",
			configMaps: []*corev1.ConfigMap{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-template",
					Namespace: "default",
				},
				Data: map[string]string{"job-template.yaml": validJobYAML},
			}},
			cmName: "worker-template",
		},
		{
			name:       "configmap not found",
			configMaps: nil,
			cmName:     "missing",
			wantErr:    "dispatch: get configmap missing",
		},
		{
			name: "missing yaml key",
			configMaps: []*corev1.ConfigMap{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-keys",
					Namespace: "default",
				},
				Data: map[string]string{"other.yaml": "foo"},
			}},
			cmName:  "bad-keys",
			wantErr: `has no key "job-template.yaml"`,
		},
		{
			name: "invalid yaml",
			configMaps: []*corev1.ConfigMap{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bad-yaml",
					Namespace: "default",
				},
				Data: map[string]string{"job-template.yaml": "not: valid: yaml: ["},
			}},
			cmName:  "bad-yaml",
			wantErr: "dispatch: decode job template",
		},
		{
			name: "no containers",
			configMaps: []*corev1.ConfigMap{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-containers",
					Namespace: "default",
				},
				Data: map[string]string{"job-template.yaml": `apiVersion: batch/v1
kind: Job
metadata:
  name: test
spec:
  template:
    spec:
      restartPolicy: Never
      containers: []
`},
			}},
			cmName:  "no-containers",
			wantErr: "dispatch: job template has no containers",
		},
		{
			name: "no grpc port",
			configMaps: []*corev1.ConfigMap{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-grpc-port",
					Namespace: "default",
				},
				Data: map[string]string{"job-template.yaml": `apiVersion: batch/v1
kind: Job
metadata:
  name: test
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: worker
        image: glyphoxa/worker:latest
        ports:
        - name: http
          containerPort: 8080
`},
			}},
			cmName:  "no-grpc-port",
			wantErr: `must have a port named "grpc"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := fake.NewSimpleClientset()
			for _, cm := range tt.configMaps {
				_, err := client.CoreV1().ConfigMaps("default").Create(
					context.Background(), cm, metav1.CreateOptions{},
				)
				if err != nil {
					t.Fatalf("setup: create configmap: %v", err)
				}
			}

			job, err := LoadJobTemplate(context.Background(), client, "default", tt.cmName)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !containsStr(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if job.Name != "glyphoxa-worker-SESSION_ID" {
				t.Errorf("got name %q, want %q", job.Name, "glyphoxa-worker-SESSION_ID")
			}
			if len(job.Spec.Template.Spec.Containers) != 1 {
				t.Errorf("got %d containers, want 1", len(job.Spec.Template.Spec.Containers))
			}
		})
	}
}

func TestStampJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sessionID string
		tenantID  string
		wantName  string
	}{
		{
			name:      "uuid session id",
			sessionID: "550e8400-e29b-41d4-a716-446655440000",
			tenantID:  "tenant-1",
			wantName:  "glyphoxa-worker-550e8400",
		},
		{
			name:      "short session id",
			sessionID: "abc",
			tenantID:  "tenant-2",
			wantName:  "glyphoxa-worker-abc",
		},
		{
			name:      "mixed case stripped",
			sessionID: "ABCD-EFGH-1234",
			tenantID:  "tenant-3",
			wantName:  "glyphoxa-worker-abcdefgh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			template := makeTestJobTemplate()
			job := StampJob(template, tt.sessionID, tt.tenantID)

			if job.Name != tt.wantName {
				t.Errorf("name = %q, want %q", job.Name, tt.wantName)
			}

			if got := job.Labels["glyphoxa.io/session-id"]; got != tt.sessionID {
				t.Errorf("job label session-id = %q, want %q", got, tt.sessionID)
			}
			if got := job.Labels["glyphoxa.io/tenant-id"]; got != tt.tenantID {
				t.Errorf("job label tenant-id = %q, want %q", got, tt.tenantID)
			}

			if got := job.Spec.Template.Labels["glyphoxa.io/session-id"]; got != tt.sessionID {
				t.Errorf("pod label session-id = %q, want %q", got, tt.sessionID)
			}
			if got := job.Spec.Template.Labels["glyphoxa.io/tenant-id"]; got != tt.tenantID {
				t.Errorf("pod label tenant-id = %q, want %q", got, tt.tenantID)
			}

			envs := job.Spec.Template.Spec.Containers[0].Env
			found := false
			for _, env := range envs {
				if env.Name == "GLYPHOXA_SESSION_ID" && env.Value == tt.sessionID {
					found = true
					break
				}
			}
			if !found {
				t.Error("GLYPHOXA_SESSION_ID env var not found in container")
			}

			// Original template is not mutated.
			if template.Name != "glyphoxa-worker-SESSION_ID" {
				t.Error("StampJob mutated the original template")
			}
		})
	}
}

func TestSanitizeForDNS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"550e8400-e29b-41d4-a716-446655440000", "550e8400"},
		{"ABCDEFGHIJKL", "abcdefgh"},
		{"a-b-c", "abc"},
		{"abc", "abc"},
		{"", ""},
		{"---!!!---", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeForDNS(tt.input); got != tt.want {
				t.Errorf("sanitizeForDNS(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// makeTestJobTemplate returns a minimal valid Job template for testing.
func makeTestJobTemplate() *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: "glyphoxa-worker-SESSION_ID",
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "worker",
						Image: "glyphoxa/worker:latest",
						Ports: []corev1.ContainerPort{{
							Name:          "grpc",
							ContainerPort: 50051,
						}},
					}},
				},
			},
		},
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
