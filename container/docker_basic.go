package container

import (
  "os"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
  "github.com/docker/docker/api/types/image"
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


// ensureImage checks if there is an image of the container
// if not it pulls it.
func (dm *DockerManager) EnsureImage(imageName string) error {
    ctx := context.Background()

    // Inspect the image to check if it exists
    if _, _, err := dm.cli.ImageInspectWithRaw(ctx, imageName); err == nil {
        return nil // Image exists, no need to pull
    }

    // pull
    reader, err := dm.cli.ImagePull(ctx, imageName, image.PullOptions{}) 
    if err != nil {
        return fmt.Errorf("failed to pull image %s: %w", imageName, err)
    }
    defer reader.Close()
    // close the stream
    if _, err := io.Copy(os.Stdout, reader); err != nil {
        return fmt.Errorf("failed to read pull response for %s: %w", imageName, err)
    }
    return nil
}

// Function to create a container
func (dm *DockerManager) Create(config ContainerConfig) (string, error) {

  // Ensure the image is available
  if err := dm.EnsureImage(config.Image); err != nil {
    return "", err
  }

	ctx := context.Background()
	containerConfig := &container.Config{
		Image: config.Image,
		Cmd:   config.Cmd,
		Tty:   config.Tty,
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
	if err := dm.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &[]int{timeout}[0]}); err != nil {
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

// Function to get stats from docker container and format them
func (dm *DockerManager) GetStats(containerID string) (string, error) {
	ctx := context.Background()
	stats, err := dm.cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to get stats: %v", err)
	}

	statsBytes, err := io.ReadAll(stats.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read stats: %v", err)
	}
	defer stats.Body.Close()

	var statsData container.StatsResponse
	if err := json.Unmarshal(statsBytes, &statsData); err != nil {
		return "", fmt.Errorf("failed to parse stats: %v", err)
	}

	statsFormatted, err := json.MarshalIndent(statsData, "", " ")
	if err != nil {
		return "", fmt.Errorf("failed to format stats: %v", err)
	}

	return string(statsFormatted), nil
}

func (dm *DockerManager) Ping() error {
  ctx := context.Background()
  _, err := dm.cli.Ping(ctx)
  return err
}
