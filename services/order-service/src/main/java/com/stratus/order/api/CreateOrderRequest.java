package com.stratus.order.api;

import com.fasterxml.jackson.annotation.JsonProperty;
import com.stratus.order.domain.OrderItem;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Email;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotEmpty;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.util.List;

/** Body of POST /orders. */
public record CreateOrderRequest(
        @NotEmpty @Size(max = 50) List<@Valid OrderItem> items,
        @NotNull @Pattern(regexp = "[A-Z]{3}") String currency,
        @JsonProperty("card_number") @NotNull @Pattern(regexp = "[0-9]{12,19}") String cardNumber,
        @NotBlank @Email String email) {

    /** Never print card numbers or emails, even if this object ends up in a log line. */
    @Override
    public String toString() {
        return "CreateOrderRequest[items=" + items + ", currency=" + currency
                + ", cardNumber=****, email=****]";
    }
}