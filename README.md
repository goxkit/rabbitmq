# GoXkit RabbitMQ

Package rabbitmq provides a comprehensive set of utilities for working with RabbitMQ in Go applications. It offers abstractions for connections, channels, exchanges, queues, bindings, publishers, and message consumers.

## Features

- **Connection Management**: Simplified RabbitMQ connection establishment and error handling
- **Channel Operations**: Abstracted AMQP channel operations for exchange and queue operations
- **Topology Definition**: Fluent API for defining exchanges, queues, and bindings
- **Publishing**: Simple message publishing with automatic headers, IDs, and tracing support
- **Consuming**: Message consumption with automatic retry, error handling, and dead-letter queuing
- **Observability**: Built-in support for tracing and logging

## Installation

```bash
go get github.com/goxkit/rabbitmq
```

## Components

### Connection & Channel

The package provides abstractions for RabbitMQ connections and channels:

```go
// Create a new RabbitMQ connection and channel
conn, ch, err := rabbitmq.NewConnection(configs)
if err != nil {
    log.Fatal(err)
}
defer conn.Close()
```

### Topology Definition

The package offers a fluent API for defining RabbitMQ topology:

```go
// Create and configure a topology
topology := rabbitmq.NewTopology(configs).
    Channel(ch).
    Exchange(rabbitmq.NewDirectExchange("orders")).
    Queue(rabbitmq.NewQueue("orders").WithDQL().WithRetry(5*time.Second, 3)).
    QueueBinding(rabbitmq.NewQueueBinding().Queue("orders").Exchange("orders").RoutingKey("new-order"))

// Apply the topology to the RabbitMQ broker
if _, err := topology.Apply(); err != nil {
    log.Fatal(err)
}
```

### Publishing Messages

The package simplifies message publishing with automatic headers and tracing:

```go
// Create a publisher
publisher := rabbitmq.NewPublisher(configs, ch)

// Publish a message
exchange := "orders"
msg := OrderCreated{ID: "123", Amount: 99.99}
err := publisher.Publish(ctx, &exchange, nil, nil, msg)
```

### Consuming Messages

The package provides a dispatcher for consuming messages with automatic retry and error handling:

```go
// Create a dispatcher
dispatcher := rabbitmq.NewDispatcher(configs, ch, topology.GetQueuesDefinition())

// Register a message handler
type OrderCreated struct {
    ID     string  `json:"id"`
    Amount float64 `json:"amount"`
}

err := dispatcher.Register("orders", OrderCreated{}, func(ctx context.Context, msg any, metadata any) error {
    order := msg.(*OrderCreated)
    // Process the order
    return nil
})

// Start consuming messages
dispatcher.ConsumeBlocking()
```

## File Documentation

### rabbitmq.go

Core package file that provides the package description and a utility function for consistent log message formatting.

### connection.go

Defines the `RMQConnection` interface for RabbitMQ connections, abstracting AMQP connection operations.

### channel.go

Defines the `AMQPChannel` interface and provides the `NewConnection` function to establish a connection and create a channel.

### errors.go

Defines custom error types and constants used throughout the package for consistent error handling.

### exchange.go

Provides types and functions for defining exchanges with different kinds (direct, fanout) and properties.

### binding.go

Defines types for exchange-to-exchange and exchange-to-queue bindings, allowing for routing configuration.

### queue.go

Defines the `QueueDefinition` type and methods for configuring queues with TTL, DLQ (Dead Letter Queue), and retry mechanisms.

### topology.go

Provides the `Topology` interface and implementation for defining and applying RabbitMQ topology configurations.

### publisher.go

Implements message publishing with automatic headers, tracing, and content type setting.

### dispatcher.go

Implements message consumption with automatic retry, error handling, and dead-letter queuing.

## Example

```go
package main

import (
    "context"
    "log"
    "time"

    configsBuilder "github.com/goxkit/configs_builder"
    "github.com/goxkit/rabbitmq"
)

type OrderCreated struct {
    ID     string  `json:"id"`
    Amount float64 `json:"amount"`
}

func main() {
    // Load configuration
    cfg := configsBuilder.NewConfigsBuilder().
        RabbitMQ().
        Otel().
        Build()

    // Create connection and channel
    conn, ch, err := rabbitmq.NewConnection(cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer conn.Close()

    // Define topology
    topology := rabbitmq.NewTopology(cfg).
        Channel(ch).
        Exchange(rabbitmq.NewDirectExchange("orders")).
        Queue(rabbitmq.NewQueue("orders").WithDQL().WithRetry(5*time.Second, 3)).
        QueueBinding(rabbitmq.NewQueueBinding().Queue("orders").Exchange("orders").RoutingKey("new-order"))

    // Apply topology
    if _, err := topology.Apply(); err != nil {
        log.Fatal(err)
    }

    // Create publisher
    publisher := rabbitmq.NewPublisher(cfg, ch)

    // Create dispatcher
    dispatcher := rabbitmq.NewDispatcher(cfg, ch, topology.GetQueuesDefinition())

    // Register handler
    err = dispatcher.Register("orders", OrderCreated{}, func(ctx context.Context, msg any, metadata any) error {
        order := msg.(*OrderCreated)
        log.Printf("Processing order: %s with amount: %.2f", order.ID, order.Amount)
        return nil
    })
    if err != nil {
        log.Fatal(err)
    }

    // Publish a message
    exchange := "orders"
    routingKey := "new-order"
    ctx := context.Background()
    err = publisher.Publish(ctx, &exchange, nil, &routingKey, OrderCreated{ID: "123", Amount: 99.99})
    if err != nil {
        log.Fatal(err)
    }

    // Start consuming messages
    dispatcher.ConsumeBlocking()
}
```

## License

MIT License

Copyright (c) 2025, The GoKit Authors
