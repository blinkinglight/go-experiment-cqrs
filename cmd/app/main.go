package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/delaneyj/toolbelt/embeddednats"
	"github.com/nats-io/nats.go"
)

func main() {
	ctx, _ := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	srv, err := embeddednats.New(ctx, embeddednats.WithDirectory("./data"), embeddednats.WithShouldClearData(false))
	if err != nil {
		panic(err)
	}

	srv.WaitForServer()
	nc, err := srv.Client()
	if err != nil {
		panic(err)
	}

	js, err := nc.JetStream()

	js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:  "lock",
		TTL:     10 * time.Second,
		History: 1,
	})

	kv, err := js.KeyValue("lock")
	if err != nil {
		panic(err)
	}

	si, err := js.AddStream(&nats.StreamConfig{
		Name:      "COMMANDS",
		Subjects:  []string{"cmds.>"},
		Storage:   nats.FileStorage,
		Retention: nats.WorkQueuePolicy,
	})
	if err != nil {
		log.Printf("Error creating stream: %v", err)
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:      "EVENTS",
		Subjects:  []string{"events.>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
	})

	_ = si
	if err != nil {
		log.Printf("Error creating stream: %v", err)
	}

	// aggregate type + id
	commandID := "agg-123"

	go func() {
		for {
			time.Sleep(1000 * time.Millisecond)
			_, err := kv.Create(commandID, nil)
			if err != nil {
				log.Printf("Command in progress")
				continue
			}
			// publish command liek users.create etc.
			_, _ = js.Publish("cmds.first", []byte("Hello, world!"))
		}
	}()

	sub, err := js.Subscribe("cmds.first", func(m *nats.Msg) {
		log.Printf("Received message: %s", m.Data)

		// unlock aggregate id
		time.Sleep(2 * time.Second)

		// publish domain event
		if _, err := js.Publish("events.first", m.Data); err != nil {
			log.Printf("Error publishing event")
			m.Nak()
			return
		}

		// unlock aggregate type + id
		_ = kv.Delete(commandID)
		_ = m.Ack()
	}, nats.BindStream("COMMANDS"), nats.ManualAck(), nats.AckExplicit(), nats.Durable("first"))
	if err != nil {
		panic(err)
	}
	defer sub.Unsubscribe()

	// subscribe for domain events
	subEvents, err := js.Subscribe("events.first", func(m *nats.Msg) {
		log.Printf("EVENT: Received event: %s", m.Data)
		_ = m.Ack()
	}, nats.DeliverAll(), nats.BindStream("EVENTS"), nats.ManualAck(), nats.AckExplicit(), nats.Durable("events1"))

	defer subEvents.Unsubscribe()

	<-ctx.Done()
	log.Printf("Shutting down server...")
}
