package com.stratus.order.payment;

import java.net.http.HttpClient;
import java.util.Map;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.MediaType;
import org.springframework.http.client.JdkClientHttpRequestFactory;
import org.springframework.stereotype.Component;
import org.springframework.web.client.RestClient;
import org.springframework.web.client.RestClientException;

/** Calls payment-service. Every call has a timeout: a hung dependency must not hang us. */
@Component
public class PaymentClient {

    private static final Logger log = LoggerFactory.getLogger(PaymentClient.class);
    private static final ParameterizedTypeReference<Map<String, Object>> JSON_OBJECT =
            new ParameterizedTypeReference<>() {
            };

    private final RestClient restClient;

    public PaymentClient(PaymentProperties properties) {
        HttpClient httpClient = HttpClient.newBuilder()
                .connectTimeout(properties.timeout())
                .build();
        JdkClientHttpRequestFactory requestFactory = new JdkClientHttpRequestFactory(httpClient);
        requestFactory.setReadTimeout(properties.timeout()); // the default is to wait forever
        this.restClient = RestClient.builder()
                .baseUrl(properties.url().toString())
                .requestFactory(requestFactory)
                .build();
    }

    /** Charges the order total, once per order ID. Returns the payment ID. */
    public String charge(String orderId, long amountCents, String currency, String cardNumber, String email) {
        try {
            Map<String, Object> payment = restClient.post()
                    .uri("/payments")
                    .header("Idempotency-Key", orderId)
                    .contentType(MediaType.APPLICATION_JSON)
                    .body(Map.of(
                            "order_id", orderId,
                            "amount_cents", amountCents,
                            "currency", currency,
                            "card_number", cardNumber,
                            "email", email))
                    .retrieve()
                    .body(JSON_OBJECT);
            if (payment == null || !(payment.get("id") instanceof String paymentId)) {
                throw new PaymentUnavailableException("payment-service returned no payment id");
            }
            return paymentId;
        } catch (RestClientException e) {
            log.atWarn().setMessage("payment call failed")
                    .addKeyValue("order_id", orderId)
                    .addKeyValue("error", e.getMessage())
                    .log();
            throw new PaymentUnavailableException("payment-service unavailable", e);
        }
    }
}