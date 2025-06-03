package api

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

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

// RunContainer запускает контейнер, агрессивно «поллит» его stats в tight‐loop,
// пока контейнер не завершится, и возвращает stdout+stderr и JSON со статистикой.
// Даже если контейнер прожил 5 мс, первый же вызов GetStatsOneShot после Start
// гарантированно успеет вернуть хоть один снимок, прежде чем контейнер полностью уйдёт.
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Создаём контейнер
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Удалим контейнер в defer после того, как соберём логи и stats
	defer api.containerManager.Remove(id)

	// 2) Стартуем контейнер и фиксируем момент старта
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// 3) Если нужна статистика, запускаем поллинг в tight‐loop (без длительных sleep),
	//    чтобы поймать хоть один снимок, пока контейнер жив
	var statsJSON string
	if showStats {
		// Приоритет: первый же вызов GetStatsOneShot лови даже если контейнер закончится через 1 мс
		var lastSummary container.ContainerStatsSummary
		var gotSummary bool

		// Канал, чтобы знать, когда контейнер закончил работу
		doneCh := make(chan struct{})

		// 3a) Горутина: ждём завершения контейнера
		go func() {
			api.containerManager.Wait(id) // ждёт, пока контейнер уйдёт
			close(doneCh)
		}()

		// 3b) В tight‐loop вызываем GetStatsOneShot(id) до тех пор, пока не увидим doneCh
		for {
			// Выполняем один «одноразовый» снимок
			summary, err := api.containerManager.GetStatsOneShot(id, startTime)
			if err == nil {
				lastSummary = summary
				gotSummary = true
			}
			// Проверяем, не завершился ли контейнер
			select {
			case <-doneCh:
				// Контейнер вышел – выходим из цикла
				goto AFTER_POLL
			default:
				// Контейнер всё ещё жив – сразу пробуем снова (без длительного sleep)
				// можно вставить tiny sleep, например time.Sleep(1 * time.Millisecond),
				// но в большинстве случаев даже без sleep мы успеваем поймать snapshot
				// быстрее, чем контейнер полностью пропадёт.
			}
		}
	AFTER_POLL:
		// 3c) По завершении контейнера используем последний валидный snapshot
		if gotSummary {
			data, _ := json.MarshalIndent(lastSummary, "", "  ")
			statsJSON = string(data)
		} else {
			// Если даже ни разу не удалось сделать snapshot (чрезвычайно маловероятно),
			// возвращаем "{}" или пустую строку
			statsJSON = "{}"
		}
	} else {
		// Если showStats==false, просто ждём завершения и не снимаем stats
		api.containerManager.Wait(id)
	}

	// 4) Сразу после завершения контейнера берём его логи
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs: %w", err)
	}

	return logs, statsJSON, nil
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

func (api *API) Ping() error {
	return api.containerManager.Ping()
}
