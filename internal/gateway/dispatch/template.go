// Package dispatch manages Kubernetes Job lifecycle for worker sessions.
// It reads a Job template from a ConfigMap, stamps session-specific values,
// creates Jobs, discovers pod IPs, and handles cleanup.
package dispatch

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
)

// LoadJobTemplate reads the worker Job template from a ConfigMap.
// The ConfigMap must contain a "job-template.yaml" key with a valid
// batch/v1 Job definition that has at least one container with a port
// named "grpc".
func LoadJobTemplate(ctx context.Context, client kubernetes.Interface, namespace, configMapName string) (*batchv1.Job, error) {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("dispatch: get configmap %s: %w", configMapName, err)
	}

	yamlData, ok := cm.Data["job-template.yaml"]
	if !ok {
		return nil, fmt.Errorf("dispatch: configmap %s has no key \"job-template.yaml\"", configMapName)
	}

	decoder := scheme.Codecs.UniversalDeserializer()
	obj, _, err := decoder.Decode([]byte(yamlData), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("dispatch: decode job template: %w", err)
	}

	job, ok := obj.(*batchv1.Job)
	if !ok {
		return nil, fmt.Errorf("dispatch: configmap data is %T, want *batchv1.Job", obj)
	}

	if len(job.Spec.Template.Spec.Containers) == 0 {
		return nil, fmt.Errorf("dispatch: job template has no containers")
	}

	if !hasGRPCPort(job) {
		return nil, fmt.Errorf("dispatch: job template container must have a port named \"grpc\"")
	}

	return job, nil
}

// StampJob creates a session-specific Job from the template by deep-copying
// it and stamping the session ID into the name, labels, and environment.
func StampJob(template *batchv1.Job, sessionID, tenantID string) *batchv1.Job {
	job := template.DeepCopy()

	safeName := sanitizeForDNS(sessionID)
	job.Name = strings.ReplaceAll(job.Name, "SESSION_ID", safeName)

	if job.Labels == nil {
		job.Labels = make(map[string]string)
	}
	job.Labels["glyphoxa.io/session-id"] = sessionID
	job.Labels["glyphoxa.io/tenant-id"] = tenantID

	if job.Spec.Template.Labels == nil {
		job.Spec.Template.Labels = make(map[string]string)
	}
	job.Spec.Template.Labels["glyphoxa.io/session-id"] = sessionID
	job.Spec.Template.Labels["glyphoxa.io/tenant-id"] = tenantID

	job.Spec.Template.Spec.Containers[0].Env = append(
		job.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "GLYPHOXA_SESSION_ID", Value: sessionID},
	)

	return job
}

func hasGRPCPort(job *batchv1.Job) bool {
	for _, port := range job.Spec.Template.Spec.Containers[0].Ports {
		if port.Name == "grpc" {
			return true
		}
	}
	return false
}

// sanitizeForDNS extracts the first 8 alphanumeric characters from the ID,
// lowercased, suitable for use in DNS-safe Kubernetes resource names.
func sanitizeForDNS(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 8 {
				break
			}
		}
	}
	return b.String()
}
