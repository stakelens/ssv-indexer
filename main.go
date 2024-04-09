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
	"github.com/jackc/pgx/v5"
	"github.com/vistastaking/ssv-indexer/db"
	"go.uber.org/zap"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	conn, err := pgx.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		logger.Fatal("Unable to connect to database", zap.Error(err))
	}
	defer conn.Close(context.Background())

	queries := db.New(conn)

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

	// Create a channel for sending requests and receiving responses
	type request struct {
		payload   []byte
		responseC chan WebSocketResponse
	}

	requestC := make(chan request)

	// Set up the WebSocket connection for the /query endpoint
	queryURL := url.URL{Scheme: "ws", Host: "localhost:16000", Path: "/query"}
	logger.Info("Connecting to WebSocket for historical data", zap.String("url", queryURL.String()))

	queryConn, _, err := websocket.DefaultDialer.Dial(queryURL.String(), nil)
	if err != nil {
		logger.Fatal("Error connecting to websocket for historical data", zap.Error(err))
	}
	defer queryConn.Close()

	var activeRequests int32
	totalRequests := int32(len(publicKeys) * (endIndex - startIndex))

	go func() {
		for req := range requestC {
			if err := queryConn.WriteMessage(websocket.TextMessage, req.payload); err != nil {
				logger.Error("Error sending message through WebSocket", zap.Error(err))
				close(req.responseC)
				continue
			}

			_, message, err := queryConn.ReadMessage()
			if err != nil {
				logger.Error("Error reading message from WebSocket", zap.Error(err))
				close(req.responseC)
				continue
			}

			var response WebSocketResponse
			if err := json.Unmarshal(message, &response); err != nil {
				logger.Error("Error parsing JSON response", zap.Error(err))
				close(req.responseC)
				continue
			}

			req.responseC <- response
			close(req.responseC)
		}
	}()

	var wg sync.WaitGroup

	for _, publicKey := range publicKeys {
		for i := startIndex; i < endIndex; i++ {
			wg.Add(1)
			atomic.AddInt32(&activeRequests, 1)
			go func(publicKey string, i int) {
				defer wg.Done()
				defer atomic.AddInt32(&activeRequests, -1)

				payload := map[string]interface{}{
					"type": "decided",
					"filter": map[string]interface{}{
						"publicKey": publicKey,
						"role":      "ATTESTER",
						"from":      i,
						"to":        i,
					},
				}

				jsonPayload, err := json.Marshal(payload)
				if err != nil {
					logger.Error("Error marshaling JSON payload", zap.Error(err), zap.String("publicKey", publicKey), zap.Int("index", i))
					return
				}

				responseC := make(chan WebSocketResponse)
				requestC <- request{payload: jsonPayload, responseC: responseC}

				response, ok := <-responseC
				if !ok {
					logger.Error("Error receiving response from WebSocket", zap.String("publicKey", publicKey), zap.Int("index", i))
					return
				}

				processResponse(response, queries, logger, "historical")

				remaining := atomic.LoadInt32(&activeRequests)
				logger.Info("Processed request",
					zap.String("publicKey", publicKey),
					zap.Int("index", i),
					zap.Int32("remainingRequests", remaining),
					zap.Int32("totalRequests", totalRequests),
				)
			}(publicKey, i)
		}
	}

	wg.Wait()

	logger.Info("All historical requests have been processed",
		zap.Int32("totalRequests", totalRequests),
	)

	close(requestC)

	for {
		_, message, err := streamConn.ReadMessage()
		if err != nil {
			logger.Error("Error reading message from WebSocket", zap.Error(err),
				zap.String("source", "real-time"))
			break
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
