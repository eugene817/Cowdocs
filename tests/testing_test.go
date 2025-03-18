package tests 

import (
  "fmt"
	"errors"
	"testing"

	dockerContainer "github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
  "github.com/eugene817/Cowdocs/container"
)

type FakeDockerManager struct {
	CreateFn    func(config container.ContainerConfig) (string, error)
	RemoveFn    func(id string) error
	StartFn     func(id string) error
	IsRunningFn func(id string) (bool, error)
	WaitFn      func(id string) (dockerContainer.WaitResponse, error)
	GetLogsFn   func(id string) (string, error)
}

func (f *FakeDockerManager) Create(config container.ContainerConfig) (string, error) {
	return f.CreateFn(config)
}

func (f *FakeDockerManager) Remove(id string) error {
	return f.RemoveFn(id)
}

func (f *FakeDockerManager) Start(id string) error {
	return f.StartFn(id)
}

func (f *FakeDockerManager) IsRunning(id string) (bool, error) {
	return f.IsRunningFn(id)
}

func (f *FakeDockerManager) Wait(id string) (dockerContainer.WaitResponse, error) {
	return f.WaitFn(id)
}

func (f *FakeDockerManager) GetLogs(id string) (string, error) {
	return f.GetLogsFn(id)
}

type API struct {
	containerManager DockerManager
}

type DockerManager interface {
	Create(config container.ContainerConfig) (string, error)
	Remove(id string) error
	Start(id string) error
	IsRunning(id string) (bool, error)
	Wait(id string) (dockerContainer.WaitResponse, error)
	GetLogs(id string) (string, error)
}

func (api *API) RunContainer(config container.ContainerConfig) (string, error) {
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", 	
		fmt.Errorf("failed to create container: %v", err)
	}
	defer api.containerManager.Remove(id)

	if err := api.containerManager.Start(id); err != nil {
		return "", fmt.Errorf("failed to start container: %v", err)
	}

	running, err := api.containerManager.IsRunning(id)
	if err != nil {
		return "", fmt.Errorf("failed to check container status: %w", err)
	}
	if !running {
		return "", fmt.Errorf("container is not running")
	}

	if _, err := api.containerManager.Wait(id); err != nil {
		return "", fmt.Errorf("failed to wait for container: %v", err)
	}

	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", fmt.Errorf("failed to get logs: %v", err)
	}
	return logs, nil
}

func TestRunContainer(t *testing.T) {
	tests := []struct {
		name          string
		createErr     error
		startErr      error
		isRunning     bool
		isRunningErr  error
		waitErr       error
		logs          string
		getLogsErr    error
		expectedError string
		expectedLogs  string
	}{
		{
			name:         "success",
			createErr:    nil,
			startErr:     nil,
			isRunning:    true,
			isRunningErr: nil,
			waitErr:      nil,
			logs:         "container logs",
			getLogsErr:   nil,
			expectedError: "",
			expectedLogs:  "container logs",
		},
		{
			name:          "fail create",
			createErr:     errors.New("create error"),
			expectedError: "failed to create container: create error",
		},
		{
			name:          "fail start",
			createErr:     nil,
			startErr:      errors.New("start error"),
			expectedError: "failed to start container: start error",
		},
		{
			name:          "container not running",
			createErr:     nil,
			startErr:      nil,
			isRunning:     false,
			isRunningErr:  nil,
			expectedError: "container is not running",
		},
		{
			name:          "fail isRunning check",
			createErr:     nil,
			startErr:      nil,
			isRunningErr:  errors.New("isRunning error"),
			expectedError: "failed to check container status: isRunning error",
		},
		{
			name:          "fail wait",
			createErr:     nil,
			startErr:      nil,
			isRunning:     true,
			isRunningErr:  nil,
			waitErr:       errors.New("wait error"),
			expectedError: "failed to wait for container: wait error",
		},
		{
			name:          "fail get logs",
			createErr:     nil,
			startErr:      nil,
			isRunning:     true,
			isRunningErr:  nil,
			waitErr:       nil,
			logs:          "",
			getLogsErr:    errors.New("get logs error"),
			expectedError: "failed to get logs: get logs error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeManager := &FakeDockerManager{
				CreateFn: func(config container.ContainerConfig) (string, error) {
					if tc.createErr != nil {
						return "", tc.createErr
					}
					return "test-container", nil
				},
				RemoveFn: func(id string) error {
					return nil
				},
				StartFn: func(id string) error {
					return tc.startErr
				},
				IsRunningFn: func(id string) (bool, error) {
					if tc.isRunningErr != nil {
						return false, tc.isRunningErr
					}
					return tc.isRunning, nil
				},
				WaitFn: func(id string) (dockerContainer.WaitResponse, error) {
					return dockerContainer.WaitResponse{}, tc.waitErr
				},
				GetLogsFn: func(id string) (string, error) {
					return tc.logs, tc.getLogsErr
				},
			}

			api := &API{
				containerManager: fakeManager,
			}

			logs, err := api.RunContainer(container.ContainerConfig{})
			if tc.expectedError != "" {
				require.Error(t, err)
				assert.Equal(t, tc.expectedError, err.Error())
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedLogs, logs)
			}
		})
	}
}

