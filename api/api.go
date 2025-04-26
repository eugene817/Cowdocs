package api

import (
	"fmt"
	"sync"

	"github.com/eugene817/Cowdocs/container"
)

// API struct with containerManager field
type API struct {
  containerManager container.Manager
}

// Function to create a new API instance
func NewAPI(mgr container.Manager) *API {
  return &API{containerManager: mgr}
}

// Function to run a container and return the logs
// Creates. Starts, Waits and Removes the container.
// The showStats flag is for executing stats reader or not
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
    id, err := api.containerManager.Create(config)
    if err != nil {
        return "", "", fmt.Errorf("failed to create container: %v", err)
    }
    defer api.containerManager.Remove(id)

    if err := api.containerManager.Start(id); err != nil {
        return "","",  fmt.Errorf("failed to start container: %v", err)
    }

    statsReturn, err := api.containerManager.GetStats(id) 
        if err != nil {
          return "", "", fmt.Errorf("failed to get stats: %v", err)
        }
    
    if _, err := api.containerManager.Wait(id); err != nil { 
        return "","",  fmt.Errorf("failed to wait for container: %v", err)
    }
    
    logs, err := api.containerManager.GetLogs(id)
    if err != nil {
        return "","",  fmt.Errorf("failed to get logs: %v", err)
    }
  
    if showStats {
      return logs, statsReturn, nil
    }
    return logs, "", nil
}

// Function to run containers in parallel using goroutines and channels.
// The container logs are sent to the channel.
// Creates. Starts, Waits and Removes the container.
func (api *API) RunContainerParallel(config container.ContainerConfig, wg *sync.WaitGroup, c chan string) error {
    defer wg.Done()
    id, err := api.containerManager.Create(config)
    if err != nil {
        return fmt.Errorf("failed to create container: %v", err)
    }
    defer api.containerManager.Remove(id)

    if err := api.containerManager.Start(id); err != nil {
        return fmt.Errorf("failed to start container: %v", err)
    }
 
    if _, err := api.containerManager.Wait(id); err != nil { 
        return fmt.Errorf("failed to wait for container: %v", err)
    }
    
    logs, err := api.containerManager.GetLogs(id)
    if err != nil {
        return fmt.Errorf("failed to get logs: %v", err)
    }
    c <- logs
    return nil
}


func (api *API) EnsureImages(images []string) error {
    for _, image := range images {
        if err := api.containerManager.EnsureImage(image); err != nil {
            return fmt.Errorf("failed to ensure image %s: %v", image, err)
        }
    }
    return nil
}
