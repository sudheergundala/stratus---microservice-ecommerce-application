package com.stratus.order.domain;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.List;

/** A placed order. It never holds card data. */
public record Order(
        String id,
        OrderStatus status,
        List<OrderItem> items,
        @JsonProperty("total_cents") long totalCents,
        String currency,
        @JsonProperty("payment_id") String paymentId,
        @JsonProperty("created_at") String createdAt) {
}