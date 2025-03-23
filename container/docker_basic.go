package container

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	dockerContainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerManager struct with cli field
type DockerManager struct {
  cli *client.Client
}

// Docker manager constructor
func NewDockerManager() (*DockerManager, error) {
  cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
  if err != nil {
    return nil, err
  }
  return &DockerManager{cli: cli}, nil
}

// Function to create a container
func (dm *DockerManager) Create(config ContainerConfig) (string, error) {
  ctx := context.Background()
  containerConfig := &dockerContainer.Config{
    Image: config.Image,
    Cmd:   config.Cmd,
    Tty: config.Tty,
  }
  resp, err := dm.cli.ContainerCreate(ctx, containerConfig, nil, nil, nil, "")
  if err != nil {
    return "", err
  }
  return resp.ID, nil
}

// Function to start a container
func (dm *DockerManager) Start(containerID string) error {
    ctx := context.Background()
    if err := dm.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
        return fmt.Errorf("failed to start container: %v", err)
    }
    return nil
}

// Function to stop a container
func (dm *DockerManager) Stop(containerID string, timeout int) error {
    ctx := context.Background()
   	if err := dm.cli.ContainerStop(ctx, containerID, dockerContainer.StopOptions{Timeout: &[]int{timeout}[0]}); err != nil {
        return fmt.Errorf("failed to stop container: %v", err)
    }
    return nil
}

// Function to remove a container
func (dm *DockerManager) Remove(containerID string) error {
    ctx := context.Background()
    if err := dm.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
        return fmt.Errorf("failed to remove container: %v", err)
    }
    return nil
}

// Function to wait for a container
func (dm *DockerManager) Wait(containerID string) (container.WaitResponse, error) {
    ctx := context.Background()
    respC, errC := dm.cli.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)

    select {
    case resp := <-respC:
        return resp, nil
    case err := <-errC:
        return container.WaitResponse{}, fmt.Errorf("failed to wait for container: %w", err)
    }
}


// Function to check if a container is running
func (dm *DockerManager) IsRunning(containerID string) (bool, error) {
    ctx := context.Background()
    inspect, err := dm.cli.ContainerInspect(ctx, containerID)
    if err != nil {
        return false, fmt.Errorf("failed to inspect container: %v", err)
    }
    return inspect.State.Running, nil
}


// Function to get logs from a container
func (dm *DockerManager) GetLogs(containerID string) (string, error) {
    ctx := context.Background()
    logsReader, err := dm.cli.ContainerLogs(ctx, containerID, container.LogsOptions{ShowStdout: true, ShowStderr: true})
    if err != nil {
        return "", fmt.Errorf("failed to get logs: %v", err)
    }
    defer logsReader.Close()

    var stdout, stderr bytes.Buffer
    if _, err := stdcopy.StdCopy(&stdout, &stderr, logsReader); err != nil {
        return "", fmt.Errorf("failed to read logs: %v", err)
    }

    if stderr.Len() > 0 {
        return "", fmt.Errorf("error in logs: %s", stderr.String())
    }

    return strings.TrimSpace(stdout.String()), nil
}

func (dm *DockerManager) GetStats(containerID string) (dockerContainer.StatsResponseReader, error) {
    ctx := context.Background()
    stats, err := dm.cli.ContainerStatsOneShot(ctx, containerID)
    if err != nil {
        return dockerContainer.StatsResponseReader{}, fmt.Errorf("failed to get stats: %v", err)
    }
    return stats, nil
}
