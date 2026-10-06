package repo

// Integration tests against a real DynamoDB API (DynamoDB Local).
// They are skipped unless DYNAMODB_ENDPOINT is set, e.g.:
//
//	DYNAMODB_ENDPOINT=http://localhost:8000 AWS_REGION=us-east-1 \
//	AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local go test ./internal/repo/ -v

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/stratus/inventory-service/internal/model"
)

func newTestStore(t *testing.T) *DynamoStore {
	t.Helper()
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		t.Skip("DYNAMODB_ENDPOINT not set; skipping DynamoDB integration test")
	}
	ctx := context.Background()
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	db := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) })

	// A fresh table per test, so tests never see each other's data.
	table := fmt.Sprintf("inventory-test-%d", time.Now().UnixNano())
	_, err = db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            &table,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &table}) })
	return NewDynamoStore(db, table)
}

func TestDynamoReserveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.SetStock(ctx, "A1", 5)
	_ = s.SetStock(ctx, "B2", 1)

	// B2 has only 1, so the whole transaction must fail and A1 must stay untouched.
	_, err := s.Reserve(ctx, model.ReservationRequest{OrderID: "o1", Items: []model.Item{{SKU: "A1", Quantity: 2}, {SKU: "B2", Quantity: 3}}})
	if !errors.Is(err, ErrOutOfStock) {
		t.Fatalf("err = %v, want ErrOutOfStock", err)
	}
	if a, _ := s.GetStock(ctx, "A1"); a.Available != 5 || a.Reserved != 0 {
		t.Fatalf("A1 = %+v after a failed transaction, want 5/0", a)
	}
}

func TestDynamoReplayReleaseCommit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.SetStock(ctx, "A1", 5)
	req := model.ReservationRequest{OrderID: "o1", Items: []model.Item{{SKU: "A1", Quantity: 2}}}

	if created, err := s.Reserve(ctx, req); err != nil || !created {
		t.Fatalf("reserve: created=%v err=%v", created, err)
	}
	if created, err := s.Reserve(ctx, req); err != nil || created {
		t.Fatalf("replay: created=%v err=%v, want false/nil", created, err)
	}
	if a, _ := s.GetStock(ctx, "A1"); a.Available != 3 || a.Reserved != 2 {
		t.Fatalf("after reserve+replay A1 = %+v, want 3/2 (replay must not reserve twice)", a)
	}
	if err := s.Release(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, "o1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second release err = %v, want ErrNotFound", err)
	}
	if a, _ := s.GetStock(ctx, "A1"); a.Available != 5 || a.Reserved != 0 {
		t.Fatalf("after release A1 = %+v, want 5/0", a)
	}

	_, _ = s.Reserve(ctx, model.ReservationRequest{OrderID: "o2", Items: []model.Item{{SKU: "A1", Quantity: 2}}})
	if err := s.Commit(ctx, "o2"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.GetStock(ctx, "A1"); a.Available != 3 || a.Reserved != 0 {
		t.Fatalf("after commit A1 = %+v, want 3/0", a)
	}
}

// 20 orders race for the last 3 units: exactly 3 may win. This is the
// "no overselling" guarantee conditional writes give across replicas.
func TestDynamoNoOverselling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.SetStock(ctx, "HOT", 3)

	var wg sync.WaitGroup
	var won, soldOut atomic.Int32
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := model.ReservationRequest{
				OrderID: fmt.Sprintf("race-%d", i),
				Items:   []model.Item{{SKU: "HOT", Quantity: 1}},
			}
			// DynamoDB may cancel a transaction that collides with another one
			// in flight (TransactionConflict). Callers retry, so the test does too.
			for attempt := 1; attempt <= 10; attempt++ {
				_, err := s.Reserve(ctx, req)
				switch {
				case err == nil:
					won.Add(1)
					return
				case errors.Is(err, ErrOutOfStock):
					soldOut.Add(1)
					return
				}
				time.Sleep(time.Duration(attempt*20) * time.Millisecond)
			}
			t.Errorf("order %d: still failing after retries", i)
		}()
	}
	wg.Wait()

	stock, _ := s.GetStock(ctx, "HOT")
	if won.Load() != 3 || stock.Available != 0 || stock.Reserved != 3 {
		t.Fatalf("won=%d soldOut=%d stock=%+v, want exactly 3 winners and 0 available", won.Load(), soldOut.Load(), stock)
	}
}
