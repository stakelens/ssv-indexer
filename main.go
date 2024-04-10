package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vistastaking/ssv-indexer/db"
	"go.uber.org/zap"
)

const maxConnections = 100

type connectionPool struct {
	connections       chan *websocket.Conn
	activeConnections int32
	logger            *zap.Logger
}

func newConnectionPool(logger *zap.Logger) *connectionPool {
	pool := &connectionPool{
		connections: make(chan *websocket.Conn, maxConnections),
		logger:      logger,
	}

	// Prepopulate the pool
	for i := 0; i < maxConnections; i++ {
		conn, err := createNewConnection(pool.logger)
		if err != nil {
			pool.logger.Error("Failed to create a new connection during prepopulation", zap.Error(err))
			continue
		}
		pool.connections <- conn
		atomic.AddInt32(&pool.activeConnections, 1)
	}

	return pool
}

func (p *connectionPool) getConnection() (*websocket.Conn, error) {
	select {
	case conn := <-p.connections:
		// Reusing an existing connection.
		return conn, nil
	default:
		// Need to create a new connection if we haven't reached the max limit.
		if atomic.LoadInt32(&p.activeConnections) < maxConnections {
			conn, err := createNewConnection(p.logger)
			if err != nil {
				p.logger.Error("Failed to create a new connection", zap.Error(err))
				return nil, err
			}
			// Successfully created a new connection, so increment the counter.
			atomic.AddInt32(&p.activeConnections, 1)
			return conn, nil
		}
		// If we've reached the max limit, block until a connection becomes available.
		conn := <-p.connections
		return conn, nil
	}
}

func (p *connectionPool) releaseConnection(conn *websocket.Conn) {
	select {
	case p.connections <- conn:
		// Successfully returned the connection to the pool.
	default:
		// Pool is full; close the connection and decrement the counter.
		conn.Close()
		atomic.AddInt32(&p.activeConnections, -1)
		p.logger.Info("Closed a connection because the pool is full", zap.Int32("activeConnections", atomic.LoadInt32(&p.activeConnections)))
	}
}

func createNewConnection(logger *zap.Logger) (*websocket.Conn, error) {
	queryURL := url.URL{Scheme: "ws", Host: "localhost:16000", Path: "/query"}
	conn, _, err := websocket.DefaultDialer.Dial(queryURL.String(), nil)
	if err != nil {
		logger.Error("Failed to dial a new WebSocket connection", zap.Error(err))
		return nil, err
	}
	return conn, nil
}

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	dbConn, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Fatal("Unable to connect to database", zap.Error(err))
	}
	defer dbConn.Close()

	queries := db.New(dbConn)

	streamURL := url.URL{Scheme: "ws", Host: "localhost:16000", Path: "/stream"}
	logger.Info("Connecting to WebSocket for real-time data", zap.String("url", streamURL.String()))

	streamConn, _, err := websocket.DefaultDialer.Dial(streamURL.String(), nil)
	if err != nil {
		logger.Fatal("Error connecting to websocket for real-time data", zap.Error(err))
	}
	defer streamConn.Close()

	_, message, err := streamConn.ReadMessage()
	if err != nil {
		logger.Fatal("Error reading message from WebSocket", zap.Error(err))
	}

	// Start real-time data listener in a new goroutine
	go func() {
		logger.Info("Listening for real-time data...")
		for {
			_, message, err := streamConn.ReadMessage()
			if err != nil {
				logger.Error("Error reading message from WebSocket", zap.Error(err),
					zap.String("source", "real-time"))
				continue
			}

			var response WebSocketResponse
			err = json.Unmarshal(message, &response)
			if err != nil {
				logger.Error("Error parsing JSON", zap.Error(err),
					zap.String("source", "real-time"))
				continue
			}

			processResponse(response, queries, logger, "real-time")
		}
	}()

	var response WebSocketResponse
	err = json.Unmarshal(message, &response)
	if err != nil {
		logger.Fatal("Error parsing JSON", zap.Error(err))
	}

	publicKeys := []string{
		"839d8cb89121aa3cfbd5c6f2cb44611e7689bd8d43c45c19398adcfbe7085404093ba2cb46f32d7b246099fef3ba1835",
		"b2e5fb301adcf8470b7e72e011aa9e453aea25a6928522257fde9c772f5471626a2a44d165e254ab1eee64a0fa322007",
		"ac649ae2598870994d8da99c1db613d8829498abd723bcbffd947c09b86caa69ec461a56bdf09e3960e5987847a5c76a",
	}
	startIndex := 1389687
	endIndex := response.Filter.From

	if endIndex < startIndex {
		logger.Fatal("Invalid start and end indices",
			zap.Int("startIndex", startIndex),
			zap.Int("endIndex", endIndex),
		)
	}

	logger.Info("Starting indexing process in 5 seconds...",
		zap.Int("startIndex", startIndex),
		zap.Int("endIndex", endIndex),
	)
	time.Sleep(5 * time.Second)

	type request struct {
		payload []byte
	}

	totalRequests := int32(len(publicKeys) * (endIndex - startIndex))
	requestC := make(chan request, totalRequests)

	connPool := newConnectionPool(logger)

	var remainingRequests int32 = totalRequests
	var wg sync.WaitGroup
	for i := 0; i < maxConnections; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for req := range requestC {
				conn, err := connPool.getConnection()
				if err != nil {
					logger.Error("Failed to get connection from pool", zap.Error(err))
					continue
				}

				err = conn.WriteMessage(websocket.TextMessage, req.payload)
				if err != nil {
					logger.Error("Failed to write message", zap.Error(err))
					connPool.releaseConnection(conn)
					continue
				}

				_, message, err := conn.ReadMessage()
				if err != nil {
					logger.Error("Failed to read message", zap.Error(err))
					connPool.releaseConnection(conn)
					continue
				}

				var response WebSocketResponse
				if err := json.Unmarshal(message, &response); err != nil {
					logger.Error("Failed to unmarshal response", zap.Error(err))
					connPool.releaseConnection(conn)
					continue
				}

				processResponse(response, queries, logger, "historical")

				connPool.releaseConnection(conn)

				atomic.AddInt32(&remainingRequests, -1)
				logger.Info("Processed request",
					zap.Int32("remainingRequests", atomic.LoadInt32(&remainingRequests)),
					zap.Int32("totalRequests", totalRequests),
				)
			}
		}()
	}

	// Generate requests
	for _, publicKey := range publicKeys {
		for i := startIndex; i < endIndex; i++ {
			payload, err := json.Marshal(map[string]interface{}{
				"type": "decided",
				"filter": map[string]interface{}{
					"publicKey": publicKey,
					"role":      "ATTESTER",
					"from":      i,
					"to":        i,
				},
			})

			if err != nil {
				logger.Error("Failed to marshal payload", zap.Error(err))
				continue
			}

			requestC <- request{
				payload: payload,
			}
		}
	}

	close(requestC) // No more requests to send, close the channel
	wg.Wait()       // Wait for all response processing goroutines to finish

	logger.Info("Finished syncing historical data",
		zap.Int("startIndex", startIndex),
		zap.Int("endIndex", endIndex),
	)

	// Prevent main from exiting to keep real-time listener running
	select {}
}

func processResponse(response WebSocketResponse, queries *db.Queries, logger *zap.Logger, source string) {
	if response.Type == "decided" {
		publicKey := response.Filter.PublicKey
		from := response.Filter.From
		to := response.Filter.To
		role := response.Filter.Role

		var data []Data
		var dataStr []string
		err := json.Unmarshal(response.Data, &data)
		if err != nil {
			err = json.Unmarshal(response.Data, &dataStr)
			if err != nil {
				logger.Error("Error parsing data field", zap.Error(err),
					zap.String("publicKey", publicKey),
					zap.Int("from", from),
					zap.Int("to", to),
					zap.String("role", role),
					zap.String("source", source))
				return
			}
			if len(dataStr) > 0 && dataStr[0] == "no messages" {
				return
			}
		}

		if len(data) > 0 {
			signers := data[0].Signers
			signature := data[0].Signature
			round := data[0].Message.Round
			root := data[0].Message.Root
			height := data[0].Message.Height

			arg := db.CreateIndexedDataParams{
				PublicKey: publicKey,
				FromValue: int32(from),
				ToValue:   int32(to),
				Role:      role,
				Signers:   int32SliceToInt32Array(signers),
				Signature: signature,
				Round:     int32(round),
				Root:      int32SliceToInt32Array(root),
				Height:    int32(height),
			}
			err := queries.CreateIndexedData(context.Background(), arg)
			if err != nil {
				logger.Error("Error inserting data into database", zap.Error(err),
					zap.String("publicKey", publicKey),
					zap.Int("from", from),
					zap.Int("to", to),
					zap.String("role", role),
					zap.String("source", source))
			} else {
				logger.Info("New entry stored in the database", zap.Any("data", arg),
					zap.String("source", source))
			}
		} else {
			arg := db.CreateIndexedDataParams{
				PublicKey: publicKey,
				FromValue: int32(from),
				ToValue:   int32(to),
				Role:      role,
			}
			err := queries.CreateIndexedData(context.Background(), arg)
			if err != nil {
				logger.Error("Error inserting data into database", zap.Error(err),
					zap.String("publicKey", publicKey),
					zap.Int("from", from),
					zap.Int("to", to),
					zap.String("role", role),
					zap.String("source", source))
			} else {
				logger.Info("New entry stored in the database", zap.Any("data", arg),
					zap.String("source", source))
			}
		}
	}
}

func int32SliceToInt32Array(slice []int) []int32 {
	result := make([]int32, len(slice))
	for i, value := range slice {
		result[i] = int32(value)
	}
	return result
}
