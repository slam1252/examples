package queue

import (
	"sync"
	"sync/atomic"
	"time"

// ошибки обробатываются отдельно
	"project/errors"
)

// V1 - Получение обработчика очереди
func V1(ds IQueueDatasource) IQueue {
	return &v1{ds: ds}
}

type v1 struct {
	cnt    uint64
	active bool
	tasks  chan IQueueTask
	sleep  time.Duration

	wg sync.WaitGroup
	ds IQueueDatasource
}

func (q *v1) DS() IQueueDatasource { return q.ds }
func (q *v1) Enabled() bool        { return q.active || q.tasks != nil }
func (q *v1) LastCount() int       { return int(q.cnt) }

func (q *v1) Step() {
	q.cnt = 0
	q.tasks = make(chan IQueueTask)

	//Формируем обработчики задания
	for i := 0; i < q.ds.Workers(); i++ {
		q.wg.Add(1)

		go func() {
			defer q.wg.Done()
			for {
				t, ok := <-q.tasks
				if !ok {
					return
				}

				atomic.AddUint64(&q.cnt, 1)
				errors.Log(t.Execute())
			}
		}()
	}

	//получаем задания []DaemonHandler
	errors.Log(q.ds.Undone(q.tasks))
	q.wg.Wait()
	q.tasks = nil

	// Если были задачи - спать не нужно, нужно дальше идти
	if q.cnt > 0 {
		q.sleep = q.ds.Sleep().Min
		return
	}

	// Если нет задач то постепенно увеличиваем время спячки
	q.sleep = q.sleep + q.ds.Sleep().Delta
	if q.sleep > q.ds.Sleep().Max {
		q.sleep = q.ds.Sleep().Max
	}
}

func (q *v1) Start() {
	q.active = true

	for q.active {

		q.Step()

		time.Sleep(q.sleep)
	}

}

func (q *v1) Stop() {
	q.active = false
}
