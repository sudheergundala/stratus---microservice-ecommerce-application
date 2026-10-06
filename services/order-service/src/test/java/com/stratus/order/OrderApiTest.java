package com.stratus.order;

import static org.assertj.core.api.Assertions.assertThat;

import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;

/** Starts the real service on a random port, with a fake payment-service behind it. */
@SpringBootTest(webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT)
class OrderApiTest {

    static final AtomicInteger paymentCalls = new AtomicInteger();
    static final AtomicReference<String> lastPaymentBody = new AtomicReference<>("");
    static volatile String paymentMode = "ok"; // ok | down | slow
    static final HttpServer fakePayment = startFakePayment();

    static final String VALID_ORDER = """
            {"items":[{"sku":"A1","quantity":2,"unit_price_cents":1000},\
            {"sku":"B2","quantity":1,"unit_price_cents":2999}],\
            "currency":"USD","card_number":"4111111111111111","email":"sam@example.com"}""";

    @Value("${local.server.port}")
    int port;

    final HttpClient http = HttpClient.newHttpClient();

    static HttpServer startFakePayment() {
        try {
            HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
            server.createContext("/payments", exchange -> {
                paymentCalls.incrementAndGet();
                lastPaymentBody.set(new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8));
                int status = 201;
                String body = "{\"id\":\"pay_test123\",\"status\":\"captured\"}";
                if (paymentMode.equals("down")) {
                    status = 500;
                    body = "{\"error\":\"boom\"}";
                }
                if (paymentMode.equals("slow")) {
                    sleep(1500);
                }
                byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
                exchange.getResponseHeaders().add("Content-Type", "application/json");
                exchange.sendResponseHeaders(status, bytes.length);
                try (OutputStream out = exchange.getResponseBody()) {
                    out.write(bytes);
                }
            });
            server.setExecutor(Executors.newCachedThreadPool());
            server.start();
            return server;
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    static void sleep(long millis) {
        try {
            Thread.sleep(millis);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    @DynamicPropertySource
    static void paymentProperties(DynamicPropertyRegistry registry) {
        registry.add("payment.url", () -> "http://127.0.0.1:" + fakePayment.getAddress().getPort());
        registry.add("payment.timeout", () -> "500ms");
    }

    @AfterAll
    static void stopFakePayment() {
        fakePayment.stop(0);
    }

    @BeforeEach
    void reset() {
        paymentCalls.set(0);
        paymentMode = "ok";
    }

    HttpResponse<String> postOrder(String key, String body) throws Exception {
        HttpRequest.Builder request = HttpRequest.newBuilder(URI.create("http://localhost:" + port + "/orders"))
                .header("Content-Type", "application/json")
                .POST(HttpRequest.BodyPublishers.ofString(body));
        if (key != null) {
            request.header("Idempotency-Key", key);
        }
        return http.send(request.build(), HttpResponse.BodyHandlers.ofString());
    }

    HttpResponse<String> get(String path) throws Exception {
        return http.send(HttpRequest.newBuilder(URI.create("http://localhost:" + port + path)).GET().build(),
                HttpResponse.BodyHandlers.ofString());
    }

    static String orderId(String json) {
        Matcher m = Pattern.compile("\"id\":\"(ord_[a-f0-9]+)\"").matcher(json);
        assertThat(m.find()).as("order id in %s", json).isTrue();
        return m.group(1);
    }

    @Test
    void placesOrderAndChargesTheTotalOnce() throws Exception {
        HttpResponse<String> res = postOrder("k-1", VALID_ORDER);

        assertThat(res.statusCode()).isEqualTo(201);
        assertThat(res.body()).contains("\"status\":\"CONFIRMED\"", "\"total_cents\":4999", "\"payment_id\":\"pay_test123\"");
        assertThat(res.body()).doesNotContain("4111111111111111");
        assertThat(paymentCalls.get()).isEqualTo(1);
        assertThat(lastPaymentBody.get()).contains("\"amount_cents\":4999", "\"order_id\":\"" + orderId(res.body()) + "\"");
    }

    @Test
    void retryWithSameKeyReturnsSameOrderWithoutChargingAgain() throws Exception {
        String first = orderId(postOrder("k-2", VALID_ORDER).body());
        HttpResponse<String> replay = postOrder("k-2", VALID_ORDER);

        assertThat(replay.statusCode()).isEqualTo(200);
        assertThat(orderId(replay.body())).isEqualTo(first);
        assertThat(paymentCalls.get()).isEqualTo(1);
    }

    @Test
    void sameKeyWithDifferentOrderIsRejected() throws Exception {
        postOrder("k-3", VALID_ORDER);
        HttpResponse<String> res = postOrder("k-3", VALID_ORDER.replace("\"quantity\":2", "\"quantity\":5"));

        assertThat(res.statusCode()).isEqualTo(422);
    }

    @Test
    void rejectsInvalidRequests() throws Exception {
        assertThat(postOrder(null, VALID_ORDER).statusCode()).isEqualTo(400);
        assertThat(postOrder("k-4", "{not json").statusCode()).isEqualTo(400);
        assertThat(postOrder("k-5", VALID_ORDER.replace("\"USD\"", "\"usd\"")).statusCode()).isEqualTo(422);
        assertThat(postOrder("k-6", VALID_ORDER.replace("\"quantity\":2", "\"quantity\":0")).statusCode()).isEqualTo(422);
        assertThat(postOrder("k-7",
                "{\"items\":[],\"currency\":\"USD\",\"card_number\":\"4111111111111111\",\"email\":\"a@b.com\"}")
                .statusCode()).isEqualTo(422);
        assertThat(paymentCalls.get()).isZero();
    }

    @Test
    void paymentOutageReturns503AndTheSameKeyCanBeRetried() throws Exception {
        paymentMode = "down";
        assertThat(postOrder("k-8", VALID_ORDER).statusCode()).isEqualTo(503);

        paymentMode = "ok";
        assertThat(postOrder("k-8", VALID_ORDER).statusCode()).isEqualTo(201);
    }

    @Test
    void hungPaymentServiceTimesOutQuickly() throws Exception {
        paymentMode = "slow";
        long started = System.nanoTime();
        HttpResponse<String> res = postOrder("k-9", VALID_ORDER);
        long elapsedMs = (System.nanoTime() - started) / 1_000_000;

        assertThat(res.statusCode()).isEqualTo(503);
        assertThat(elapsedMs).isLessThan(1400);
    }

    @Test
    void probesAndLookups() throws Exception {
        assertThat(get("/healthz").statusCode()).isEqualTo(200);
        assertThat(get("/readyz").statusCode()).isEqualTo(200);
        String id = orderId(postOrder("k-10", VALID_ORDER).body());
        assertThat(get("/orders/" + id).statusCode()).isEqualTo(200);
        assertThat(get("/orders/ord_missing").statusCode()).isEqualTo(404);
    }
}