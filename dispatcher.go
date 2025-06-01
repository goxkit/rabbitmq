// Copyright (c) 2023, The GoKit Authors
// MIT License
// All rights reserved.

package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"syscall"

	"github.com/goxkit/configs"
	"github.com/goxkit/logging"
	tracingamqp "github.com/goxkit/tracing/amqp"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type (
	// Dispatcher defines an interface for managing RabbitMQ message consumption.
	// It provides methods to register message handlers and consume messages in a blocking manner.
	Dispatcher interface {
		// Register associates a queue with a message type and a handler function.
		// It ensures that messages from the specified queue are processed by the handler.
		// Returns an error if the registration parameters are invalid or if the queue definition is not found.
		Register(queue string, typE any, handler ConsumerHandler) error

		// ConsumeBlocking starts consuming messages and dispatches them to the registered handlers.
		// This method blocks execution until the process is terminated by a signal.
		ConsumeBlocking()
	}

	// dispatcher is the concrete implementation of the Dispatcher interface.
	// It manages the registration and execution of message handlers for RabbitMQ queues.
	dispatcher struct {
		logger              logging.Logger
		channel             AMQPChannel
		queueDefinitions    map[string]*QueueDefinition
		consumersDefinition map[string]*ConsumerDefinition
		tracer              trace.Tracer
		signalCh            chan os.Signal
	}

	// ConsumerHandler is a function type that defines message handler callbacks.
	// It receives a context (for tracing), the unmarshaled message, and metadata about the delivery.
	// Returns an error if the message processing fails.
	ConsumerHandler = func(ctx context.Context, msg any, metadata any) error

	// ConsumerDefinition represents the configuration for a consumer.
	// It holds information about the queue, message type, and handler function.
	ConsumerDefinition struct {
		queue           string
		msgType         string
		reflect         *reflect.Value
		queueDefinition *QueueDefinition
		handler         ConsumerHandler
	}

	// deliveryMetadata contains metadata extracted from an AMQP delivery.
	// This includes message ID, retry count, message type, and headers.
	deliveryMetadata struct {
		MessageId string
		XCount    int64
		Type      string
		Headers   map[string]interface{}
	}
)

// NewDispatcher creates a new dispatcher instance with the provided configuration.
// It initializes signal handling and sets up the necessary components for message consumption.
func NewDispatcher(cfgs *configs.Configs, channel AMQPChannel, queueDefinitions map[string]*QueueDefinition) *dispatcher {
	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	return &dispatcher{
		logger:              cfgs.Logger,
		channel:             channel,
		queueDefinitions:    queueDefinitions,
		consumersDefinition: map[string]*ConsumerDefinition{},
		tracer:              otel.Tracer("rmq-dispatcher"),
		signalCh:            signalCh,
	}
}

// Register associates a queue with a message type and a handler function.
// It validates the parameters and ensures that the queue definition exists.
// Returns an error if the registration parameters are invalid or if the queue definition is not found.
//
// Parameters:
//   - queue: The name of the queue to consume messages from (must match a queue in the topology)
//   - msg: A zero-value instance of the message type to consume (used for type reflection)
//   - handler: A function that processes messages of the specified type
//
// Example:
//
//	type OrderCreated struct {
//	    ID     string  `json:"id"`
//	    Amount float64 `json:"amount"`
//	}
//
//	dispatcher.Register("orders", OrderCreated{}, func(ctx context.Context, msg any, metadata any) error {
//	    order := msg.(*OrderCreated)
//	    // Process the order
//	    return nil
//	})
//
// The handler function receives:
//   - A context with tracing information
//   - The unmarshaled message (needs to be cast to the actual type)
//   - Metadata about the delivery (message ID, headers, retry count)
//
// If the handler returns an error:
//   - RetryableError: Message will be requeued for processing later
//   - Any other error: Message will be sent to the DLQ if configured
func (d *dispatcher) Register(queue string, msg any, handler ConsumerHandler) error {
	if msg == nil || queue == "" {
		return InvalidDispatchParamsError
	}

	def, ok := d.queueDefinitions[queue]
	if !ok {
		return QueueDefinitionNotFoundError
	}

	ref := reflect.New(reflect.TypeOf(msg))
	msgType := fmt.Sprintf("%T", msg)

	d.consumersDefinition[msgType] = &ConsumerDefinition{
		queue:           queue,
		msgType:         msgType,
		reflect:         &ref,
		queueDefinition: def,
		handler:         handler,
	}

	return nil
}

// ConsumeBlocking starts consuming messages from all registered queues.
// It creates a goroutine for each consumer and blocks until a termination signal is received.
// This method should be called after all Register operations are complete.
//
// The dispatcher listens for OS signals (SIGINT, SIGTERM, SIGQUIT) and will gracefully
// terminate when any of these signals are received. This makes it suitable for use
// in container environments where graceful shutdown is important.
//
// Example usage:
//
//	dispatcher := rabbitmq.NewDispatcher(configs, channel, topology.GetQueuesDefinition())
//	err := dispatcher.Register("orders", OrderCreated{}, processOrderHandler)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	dispatcher.ConsumeBlocking() // Blocks until shutdown signal
func (d *dispatcher) ConsumeBlocking() {
	for _, cd := range d.consumersDefinition {
		go d.consume(cd.queue, cd.msgType)
	}

	<-d.signalCh
	d.logger.Debug(LogMessage("signal received, closing dispatcher"))
}

// consume starts consuming messages from a specific queue.
// This internal method is responsible for the core message processing workflow:
//  1. Subscribes to the specified queue with manual acknowledgements enabled
//  2. Extracts metadata from received messages
//  3. Identifies the appropriate handler based on message type
//  4. Creates a tracing span for observability
//  5. Deserializes the message body to the registered type
//  6. Handles retry logic based on queue configuration
//  7. Calls the user-provided handler function
//  8. Manages acknowledgements and failures based on handler results
//  9. Routes failed messages to retry queues or dead-letter queues as appropriate
//
// For messages that fail processing, behavior depends on the queue configuration:
// - With retry enabled: Messages are requeued up to the specified retry limit
// - With DLQ enabled: Failed messages are published to the dead-letter queue
// - With both: Messages are retried first, then sent to the DLQ after exhausting retries
// - With neither: Messages are negatively acknowledged and requeued
func (d *dispatcher) consume(queue, msgType string) {
	delivery, err := d.channel.Consume(queue, msgType, false, false, false, false, nil)
	if err != nil {
		d.logger.Error(
			LogMessage("failure to declare consumer"),
			zap.String("queue", queue),
			zap.Error(err),
		)
		return
	}

	for received := range delivery {
		metadata, err := d.extractMetadata(&received)
		if err != nil {
			_ = received.Ack(false)
			continue
		}

		d.logger.Debug(
			LogMessage("received message: ", metadata.Type),
			zap.String("messageId", metadata.MessageId),
		)

		def, ok := d.consumersDefinition[msgType]

		if !ok {
			d.logger.Warn(
				LogMessage("could not find any consumer for this msg type"),
				zap.String("type", metadata.Type),
				zap.String("messageId", metadata.MessageId),
			)
			if err := received.Ack(false); err != nil {
				d.logger.Error(
					LogMessage("failed to ack msg"),
					zap.String("messageId", received.MessageId),
				)
			}
			continue
		}

		ctx, span := tracingamqp.NewConsumerSpan(d.tracer, received.Headers, received.Type)

		ptr := def.reflect.Interface()
		if err = json.Unmarshal(received.Body, ptr); err != nil {
			span.RecordError(err)
			d.logger.Error(
				LogMessage("unmarshal error"),
				zap.String("messageId", received.MessageId),
				zap.Any("context", ctx),
			)
			_ = received.Nack(true, false)
			span.End()
			continue
		}

		if def.queueDefinition.withRetry && metadata.XCount > def.queueDefinition.retires {
			d.logger.Warn(
				LogMessage("message reprocessed to many times, sending to dead letter"),
				zap.Any("context", ctx),
			)
			_ = received.Ack(false)

			if err = d.publishToDlq(def, &received); err != nil {
				span.RecordError(err)
				d.logger.Error(
					LogMessage("failure to publish to dlq"),
					zap.String("messageId", received.MessageId),
					zap.Any("context", ctx),
				)
			}

			span.End()
			continue
		}

		if err = def.handler(ctx, ptr, metadata); err != nil {
			d.logger.Error(
				LogMessage("error to process message"),
				zap.Error(err),
				zap.Any("context", ctx),
			)

			if def.queueDefinition.withDLQ || err != RetryableError {
				span.RecordError(err)
				_ = received.Ack(false)

				if err = d.publishToDlq(def, &received); err != nil {
					span.RecordError(err)
					d.logger.Error(
						LogMessage("failure to publish to dlq"),
						zap.String("messageId", received.MessageId),
						zap.Any("context", ctx),
					)
				}

				span.End()
				continue
			}

			d.logger.Warn(
				LogMessage("send message to process latter"),
				zap.Any("context", ctx),
			)

			_ = received.Nack(false, false)
			span.End()
			continue
		}

		d.logger.
			Debug(
				LogMessage("message processed properly"),
				zap.String("messageId", received.MessageId),
				zap.Any("context", ctx),
			)
		_ = received.Ack(true)
		span.SetStatus(codes.Ok, "success")
		span.End()
	}
}

// extractMetadata extracts relevant metadata from an AMQP delivery.
// This includes the message ID, type, and retry count.
// Returns an error if the message has unformatted headers.
//
// The metadata extraction is critical for:
// - Identifying message types for proper routing to handlers
// - Tracking retry counts for retry-enabled queues
// - Providing context for tracing and logging
// - Making headers available to message handlers
//
// The retry count (XCount) is extracted from the x-death header that
// RabbitMQ adds to messages that have been dead-lettered and requeued.
func (d *dispatcher) extractMetadata(delivery *amqp.Delivery) (*deliveryMetadata, error) {
	typ := delivery.Type
	if typ == "" {
		d.logger.Error(
			LogMessage("unformatted amqp delivery - missing type parameter"),
			zap.String("messageId", delivery.MessageId),
		)
		return nil, ReceivedMessageWithUnformattedHeaderError
	}

	var xCount int64
	if xDeath, ok := delivery.Headers["x-death"]; ok {
		v, _ := xDeath.([]interface{})
		table, _ := v[0].(amqp.Table)
		count, _ := table["count"].(int64)
		xCount = count
	}

	return &deliveryMetadata{
		MessageId: delivery.MessageId,
		Type:      typ,
		XCount:    xCount,
		Headers:   delivery.Headers,
	}, nil
}

// publishToDlq publishes a message to the dead-letter queue.
// It preserves the original message properties and headers.
//
// Dead-letter queues (DLQs) are a critical component of the error handling strategy.
// Messages are sent to DLQs in the following scenarios:
// - Message processing failed with a non-retryable error
// - Message exceeded the maximum number of retry attempts
// - Queue is configured with DLQ but not with retry mechanism
//
// The original message is preserved exactly as received, including:
// - All headers (including tracing headers)
// - Content type and message ID
// - User ID and application ID
// - The original message body
//
// This allows for later inspection, debugging, or manual reprocessing of failed messages.
func (m *dispatcher) publishToDlq(definition *ConsumerDefinition, received *amqp.Delivery) error {
	return m.channel.Publish("", definition.queueDefinition.dqlName, false, false, amqp.Publishing{
		Headers:     received.Headers,
		Type:        received.Type,
		ContentType: received.ContentType,
		MessageId:   received.MessageId,
		UserId:      received.UserId,
		AppId:       received.AppId,
		Body:        received.Body,
	})
}
