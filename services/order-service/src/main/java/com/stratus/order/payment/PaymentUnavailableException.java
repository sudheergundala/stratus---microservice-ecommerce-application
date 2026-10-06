package com.stratus.order.payment;

/** payment-service could not be reached, timed out, or returned an error. Safe to retry. */
public class PaymentUnavailableException extends RuntimeException {

    public PaymentUnavailableException(String message) {
        super(message);
    }

    public PaymentUnavailableException(String message, Throwable cause) {
        super(message, cause);
    }
}