package service

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"text/template"

	"github.com/kerezsiz42/scanner-operator2/internal/utils"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
)

//go:embed job.template.yaml
var JobTemplateYAML string

type JobObjectServiceInterface interface {
	Create(imageID string, namespace string) (*batchv1.Job, error)
}

type JobObjectService struct {
	t                  *template.Template
	decoder            runtime.Serializer
	apiServiceHostname string
}

func NewJobObjectService() (*JobObjectService, error) {
	t, err := template.New("job.template.yaml").Parse(JobTemplateYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to parse job.template.yaml: %w", err)
	}

	apiServiceHostname := os.Getenv("API_SERVICE_HOSTNAME")
	if apiServiceHostname == "" {
		return nil, errors.New("API_SERVICE_HOSTNAME environment variable not set")
	}

	return &JobObjectService{
		t:                  t,
		decoder:            yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme),
		apiServiceHostname: apiServiceHostname,
	}, nil
}

func (j *JobObjectService) Create(imageID string, namespace string) (*batchv1.Job, error) {
	jobTemplateVars := struct {
		ScanName           string
		ImageID            string
		Namespace          string
		ApiServiceHostname string
	}{
		ScanName:           fmt.Sprintf("scan-%s", utils.GenerateId()),
		ImageID:            imageID,
		Namespace:          namespace,
		ApiServiceHostname: j.apiServiceHostname,
	}

	var buf bytes.Buffer
	if err := j.t.Execute(&buf, jobTemplateVars); err != nil {
		return nil, fmt.Errorf("failed to execute variable substitution in job.template.yaml: %w", err)
	}

	job := &batchv1.Job{}
	_, _, err := j.decoder.Decode(buf.Bytes(), nil, job)
	if err != nil {
		return nil, fmt.Errorf("failed to decode buffer: %w", err)
	}

	return job, nil
}
