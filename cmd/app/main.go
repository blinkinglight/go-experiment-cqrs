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

	_, _ = kv.Create(commandID, nil)

	go func() {
		for {
			time.Sleep(2000 * time.Millisecond)
			// publish command liek users.create etc.
			_, _ = js.Publish("cmds.first", []byte("Hello, world! 1"))
			_, _ = js.Publish("cmds.first", []byte("Hello, world! 2"))
			_, _ = js.Publish("cmds.first", []byte("Hello, world! 3"))
			_, _ = js.Publish("cmds.first", []byte("Hello, world! 4"))
			_, _ = js.Publish("cmds.first", []byte("Hello, world! 5"))
			log.Printf("Published command")
			// return
		}
	}()

	js.DeleteConsumer("COMMANDS", "first")
	js.AddConsumer("COMMANDS", &nats.ConsumerConfig{
		Durable:       "first",
		FilterSubject: "cmds.>",
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverGroup:  "q",
	})

	for i := 0; i < 10; i++ {
		go func() {
			sub, err := js.PullSubscribe("cmds.>", "first", nats.BindStream("COMMANDS"), nats.ManualAck(), nats.DeliverAll())
			if err != nil {
				log.Printf("Error creating subscription: %v", err)
			}
			_ = err
			for {
				msgs, err := sub.Fetch(1, nats.MaxWait(10*time.Second))
				if err != nil {
					if err == context.DeadlineExceeded || err == nats.ErrTimeout {
						continue
					}
					log.Printf("Error fetching messages: %v", err)
					time.Sleep(1 * time.Second)
					continue
				}
				for _, msg := range msgs {
					log.Printf("Received message: %s", msg.Data)
					// unlock aggregate id

					var rev uint64
					r, err := kv.Get(commandID)
					if err != nil {
						log.Printf("%v", err)

						msg.NakWithDelay(1 * time.Second)
						return
					}
					rev = r.Revision()

					// do heavy logic here

					if rv, err := kv.Update(commandID, nil, rev); err != nil {
						log.Printf("Error unlocking aggregate id from %d to %d", rv, rev)
						nc.Publish("ui.notify.error", []byte("Error unlocking aggregate id"))
						msg.Nak()
						return
					}

					// publish domain event
					if _, err := js.Publish("events.first", msg.Data); err != nil {
						log.Printf("Error publishing event")
						msg.Nak()
						return
					}

					// unlock aggregate type + id
					_ = msg.Ack()
				}
			}
		}()
	}

	// subscribe for domain events
	subEvents, err := js.Subscribe("events.first", func(m *nats.Msg) {
		log.Printf("EVENT: Received event: %s", m.Data)
		_ = m.Ack()
	}, nats.DeliverAll(), nats.BindStream("EVENTS"), nats.ManualAck(), nats.AckExplicit(), nats.Durable("events1"))

	defer subEvents.Unsubscribe()

	<-ctx.Done()
	log.Printf("Shutting down server...")
}
