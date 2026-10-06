// Package repo stores inventory in DynamoDB.
//
// One table, two kinds of item, told apart by the partition key:
//
//	pk = "SKU#<sku>"       stock level:  available, reserved
//	pk = "RES#<order_id>"  reservation:  items, created_at
//
// Every change runs as a DynamoDB transaction with conditions, so stock can
// never go negative, even with many replicas reserving the same SKU at once.
package repo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/stratus/inventory-service/internal/model"
)

var (
	// ErrNotFound means the SKU or reservation does not exist.
	ErrNotFound = errors.New("not found")
	// ErrOutOfStock means at least one item lacks available stock; nothing was reserved.
	ErrOutOfStock = errors.New("insufficient stock")
	// ErrConflict means the order already holds a reservation with different items.
	ErrConflict = errors.New("order already has a different reservation")
)

// Store is what the HTTP handlers need. The handlers' tests use a fake.
type Store interface {
	SetStock(ctx context.Context, sku string, available int64) error
	GetStock(ctx context.Context, sku string) (model.Stock, error)
	// Reserve holds all items or none. created is false for a replay of an
	// identical, already-held reservation.
	Reserve(ctx context.Context, req model.ReservationRequest) (created bool, err error)
	// Release returns reserved stock to available (order failed).
	Release(ctx context.Context, orderID string) error
	// Commit consumes reserved stock (order paid).
	Commit(ctx context.Context, orderID string) error
}

const (
	skuPrefix = "SKU#"
	resPrefix = "RES#"
)

// DynamoStore implements Store on a DynamoDB table with partition key "pk".
type DynamoStore struct {
	db    *dynamodb.Client
	table string
}

// NewDynamoStore returns a store using the given client and table.
func NewDynamoStore(db *dynamodb.Client, table string) *DynamoStore {
	return &DynamoStore{db: db, table: table}
}

func (s *DynamoStore) SetStock(ctx context.Context, sku string, available int64) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:        &s.table,
		Key:              key(skuPrefix + sku),
		UpdateExpression: aws.String("SET available = :a, reserved = if_not_exists(reserved, :zero)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":a":    num(available),
			":zero": num(0),
		},
	})
	return err
}

func (s *DynamoStore) GetStock(ctx context.Context, sku string) (model.Stock, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            key(skuPrefix + sku),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return model.Stock{}, err
	}
	if out.Item == nil {
		return model.Stock{}, ErrNotFound
	}
	available, err := readNum(out.Item, "available")
	if err != nil {
		return model.Stock{}, err
	}
	reserved, err := readNum(out.Item, "reserved")
	if err != nil {
		return model.Stock{}, err
	}
	return model.Stock{SKU: sku, Available: available, Reserved: reserved}, nil
}

func (s *DynamoStore) Reserve(ctx context.Context, req model.ReservationRequest) (bool, error) {
	ops := make([]types.TransactWriteItem, 0, len(req.Items)+1)

	// Operation 0: the reservation record, only if this order has none yet.
	ops = append(ops, types.TransactWriteItem{Put: &types.Put{
		TableName: &s.table,
		Item: map[string]types.AttributeValue{
			"pk":         str(resPrefix + req.OrderID),
			"items":      encodeItems(req.Items),
			"created_at": str(time.Now().UTC().Format(time.RFC3339)),
		},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	}})

	// Operations 1..n: move stock from available to reserved, only if enough
	// is available. An unknown SKU fails the condition too.
	for _, it := range req.Items {
		ops = append(ops, types.TransactWriteItem{Update: &types.Update{
			TableName:                 &s.table,
			Key:                       key(skuPrefix + it.SKU),
			UpdateExpression:          aws.String("SET available = available - :q, reserved = reserved + :q"),
			ConditionExpression:       aws.String("available >= :q"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":q": num(it.Quantity)},
		}})
	}

	_, err := s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: ops})
	if err == nil {
		return true, nil
	}

	var canceled *types.TransactionCanceledException
	if !errors.As(err, &canceled) {
		return false, err
	}
	reasons := canceled.CancellationReasons
	if len(reasons) > 0 && aws.ToString(reasons[0].Code) == "ConditionalCheckFailed" {
		// The order already has a reservation: a retry, or a conflicting request.
		existing, gerr := s.reservationItems(ctx, req.OrderID)
		if gerr != nil {
			return false, gerr
		}
		if !model.SameItems(existing, req.Items) {
			return false, ErrConflict
		}
		return false, nil
	}
	for _, r := range reasons {
		if aws.ToString(r.Code) == "ConditionalCheckFailed" {
			return false, ErrOutOfStock
		}
	}
	return false, err
}

func (s *DynamoStore) Release(ctx context.Context, orderID string) error {
	return s.finish(ctx, orderID, "SET available = available + :q, reserved = reserved - :q")
}

func (s *DynamoStore) Commit(ctx context.Context, orderID string) error {
	return s.finish(ctx, orderID, "SET reserved = reserved - :q")
}

// finish deletes the reservation record and applies update to each item, in
// one transaction. The delete's condition guarantees that two concurrent
// release/commit calls cannot both apply.
func (s *DynamoStore) finish(ctx context.Context, orderID, update string) error {
	items, err := s.reservationItems(ctx, orderID)
	if err != nil {
		return err
	}
	ops := make([]types.TransactWriteItem, 0, len(items)+1)
	ops = append(ops, types.TransactWriteItem{Delete: &types.Delete{
		TableName:           &s.table,
		Key:                 key(resPrefix + orderID),
		ConditionExpression: aws.String("attribute_exists(pk)"),
	}})
	for _, it := range items {
		ops = append(ops, types.TransactWriteItem{Update: &types.Update{
			TableName:                 &s.table,
			Key:                       key(skuPrefix + it.SKU),
			UpdateExpression:          aws.String(update),
			ExpressionAttributeValues: map[string]types.AttributeValue{":q": num(it.Quantity)},
		}})
	}
	_, err = s.db.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: ops})
	var canceled *types.TransactionCanceledException
	if errors.As(err, &canceled) && len(canceled.CancellationReasons) > 0 &&
		aws.ToString(canceled.CancellationReasons[0].Code) == "ConditionalCheckFailed" {
		return ErrNotFound // finished concurrently by another call
	}
	return err
}

func (s *DynamoStore) reservationItems(ctx context.Context, orderID string) ([]model.Item, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &s.table,
		Key:            key(resPrefix + orderID),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, ErrNotFound
	}
	return decodeItems(out.Item["items"])
}

// --- DynamoDB attribute helpers -------------------------------------------

func key(pk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": str(pk)}
}

func str(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }

func num(n int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

func readNum(item map[string]types.AttributeValue, name string) (int64, error) {
	n, ok := item[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("attribute %s is missing or not a number", name)
	}
	return strconv.ParseInt(n.Value, 10, 64)
}

func encodeItems(items []model.Item) types.AttributeValue {
	list := make([]types.AttributeValue, 0, len(items))
	for _, it := range items {
		list = append(list, &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"sku":      str(it.SKU),
			"quantity": num(it.Quantity),
		}})
	}
	return &types.AttributeValueMemberL{Value: list}
}

func decodeItems(av types.AttributeValue) ([]model.Item, error) {
	list, ok := av.(*types.AttributeValueMemberL)
	if !ok {
		return nil, errors.New("reservation items attribute is missing or not a list")
	}
	items := make([]model.Item, 0, len(list.Value))
	for _, v := range list.Value {
		m, ok := v.(*types.AttributeValueMemberM)
		if !ok {
			return nil, errors.New("reservation item is not a map")
		}
		sku, ok := m.Value["sku"].(*types.AttributeValueMemberS)
		if !ok {
			return nil, errors.New("reservation item has no sku")
		}
		qty, err := readNum(m.Value, "quantity")
		if err != nil {
			return nil, err
		}
		items = append(items, model.Item{SKU: sku.Value, Quantity: qty})
	}
	return items, nil
}
