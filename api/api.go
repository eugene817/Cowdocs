package api

import (
	"fmt"

	"github.com/eugene817/Cowdocs/container"
)

type API struct {
  containerManager container.DockerManager
}

func NewAPI() (*API, error) {
  mgr, err := container.NewDockerManager()
  if err != nil {
    return nil, err
  }
  return &API{containerManager: *mgr}, nil
}

func (api *API) RunContainer(config container.ContainerConfig) (string, error) {
    id, err := api.containerManager.Create(config)
    if err != nil {
        return "", fmt.Errorf("failed to create container: %v", err)
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
