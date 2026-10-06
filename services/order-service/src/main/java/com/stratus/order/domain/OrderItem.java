package com.stratus.order.domain;

import com.fasterxml.jackson.annotation.JsonProperty;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Positive;
import jakarta.validation.constraints.Size;

/** One line of an order. Prices are integer cents. */
public record OrderItem(
        @NotBlank @Size(max = 64) String sku,
        @Positive @Max(1000) int quantity,
        @JsonProperty("unit_price_cents") @Positive long unitPriceCents) {
}