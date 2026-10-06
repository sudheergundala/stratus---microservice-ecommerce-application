package com.stratus.order.payment;

import jakarta.validation.constraints.NotNull;
import java.net.URI;
import java.time.Duration;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.validation.annotation.Validated;

/** payment.url and payment.timeout from application.yml (env: PAYMENT_SERVICE_URL, PAYMENT_TIMEOUT). */
@Validated
@ConfigurationProperties(prefix = "payment")
public record PaymentProperties(@NotNull URI url, @NotNull Duration timeout) {
}