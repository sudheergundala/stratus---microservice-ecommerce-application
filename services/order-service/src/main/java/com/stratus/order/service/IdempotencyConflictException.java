package com.stratus.order.service;

/** The Idempotency-Key was already used for a different order. */
public class IdempotencyConflictException extends RuntimeException {

    public IdempotencyConflictException() {
        super("Idempotency-Key already used with a different order");
    }
}