package com.stratus.order.api;

import com.stratus.order.payment.PaymentUnavailableException;
import com.stratus.order.service.IdempotencyConflictException;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.TreeMap;
import org.springframework.http.ResponseEntity;
import org.springframework.http.converter.HttpMessageNotReadableException;
import org.springframework.web.bind.MethodArgumentNotValidException;
import org.springframework.web.bind.MissingRequestHeaderException;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;
import org.springframework.web.server.ResponseStatusException;

/** Turns exceptions into consistent JSON errors: {"error": "...", "fields": {...}}. */
@RestControllerAdvice
public class ApiExceptionHandler {

    @ExceptionHandler(MethodArgumentNotValidException.class)
    ResponseEntity<Map<String, Object>> invalidBody(MethodArgumentNotValidException e) {
        Map<String, String> fields = new TreeMap<>();
        e.getBindingResult().getFieldErrors()
                .forEach(f -> fields.putIfAbsent(f.getField(), f.getDefaultMessage()));
        return error(422, "validation failed", fields);
    }

    @ExceptionHandler(HttpMessageNotReadableException.class)
    ResponseEntity<Map<String, Object>> unreadableBody() {
        return error(400, "request body must be valid JSON", null);
    }

    @ExceptionHandler(MissingRequestHeaderException.class)
    ResponseEntity<Map<String, Object>> missingHeader(MissingRequestHeaderException e) {
        return error(400, e.getHeaderName() + " header is required", null);
    }

    @ExceptionHandler(ResponseStatusException.class)
    ResponseEntity<Map<String, Object>> status(ResponseStatusException e) {
        return error(e.getStatusCode().value(), e.getReason(), null);
    }

    @ExceptionHandler(IdempotencyConflictException.class)
    ResponseEntity<Map<String, Object>> conflict(IdempotencyConflictException e) {
        return error(422, e.getMessage(), null);
    }

    @ExceptionHandler(PaymentUnavailableException.class)
    ResponseEntity<Map<String, Object>> paymentUnavailable() {
        return error(503, "payment service unavailable; retry with the same Idempotency-Key", null);
    }

    private static ResponseEntity<Map<String, Object>> error(int status, String message, Map<String, String> fields) {
        Map<String, Object> body = new LinkedHashMap<>();
        body.put("error", message);
        if (fields != null && !fields.isEmpty()) {
            body.put("fields", fields);
        }
        return ResponseEntity.status(status).body(body);
    }
}