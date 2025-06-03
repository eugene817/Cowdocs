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

// RunContainer теперь опрашивает stats каждые 10 мс в цикле, пока контейнер не завершится.
// Благодаря этому мы точно получим хотя бы один снимок статистики даже у очень коротких контейнеров.
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Создаём контейнер
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Обязательно удалим контейнер после всего (даже если return ранее)
	defer api.containerManager.Remove(id)

	// 2) Стартуем контейнер и фиксируем момент старта
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// 3) Если showStats, запускаем “поллинг” stats на фоне
	var statsJSON string
	if showStats {
		// Канал, по которому мы узнаем, что контейнер завершился
		doneCh := make(chan struct{})

		// Переменная, в которую будем сохранять “последний валидный снимок stats”
		var lastSummary container.ContainerStatsSummary

		// Горутина #1: каждый 10 мс вызываем GetStatsOneShot и сохраняем результат в lastSummary
		go func() {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()

			for {
				select {
				case <-doneCh:
					// Контейнер закончил работу — выходим из цикла
					return
				case <-ticker.C:
					// Снимаем “одноразовый” снимок статов
					summary, err := api.containerManager.GetStatsOneShot(id, startTime)
					if err == nil {
						lastSummary = summary
					}
					// Если ошибка — просто игнорируем (например, контейнер ещё не готов)
				}
			}
		}()

		// Горутина #2: ждём факта завершения контейнера и закрываем doneCh
		go func() {
			api.containerManager.Wait(id) // ждёт, пока контейнер не завершится
			close(doneCh)
		}()

		// 4) Дожидаемся, пока doneCh закроется (то есть контейнер завершился)
		<-doneCh

		// 5) Форматируем последний snapshot в JSON (если он вообще появился)
		if (lastSummary != container.ContainerStatsSummary{}) {
			data, _ := json.MarshalIndent(lastSummary, "", "  ")
			statsJSON = string(data)
		} else {
			// Если за всё время ни одного валидного GetStatsOneShot не получилось —
			// оставляем statsJSON пустым или делаем “{}`.
			statsJSON = "{}"
		}
	} else {
		// Если showStats==false, просто ждём завершения и ничего не делаем с stats
		api.containerManager.Wait(id)
	}

	// 6) Получаем логи контейнера (stdout+stderr)
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
