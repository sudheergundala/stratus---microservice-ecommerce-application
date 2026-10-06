package com.stratus.order.service;

import com.stratus.order.api.CreateOrderRequest;
import com.stratus.order.domain.Order;
import com.stratus.order.domain.OrderItem;
import com.stratus.order.domain.OrderStatus;
import com.stratus.order.payment.PaymentClient;
import com.stratus.order.repo.OrderRepository;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;

/** The order workflow: idempotency, totals, payment. */
@Service
public class OrderService {

    private static final Logger log = LoggerFactory.getLogger(OrderService.class);

    private final OrderRepository repository;
    private final PaymentClient payments;

    public OrderService(OrderRepository repository, PaymentClient payments) {
        this.repository = repository;
        this.payments = payments;
    }

    public OrderRepository.Result placeOrder(String idempotencyKey, CreateOrderRequest request) {
        OrderRepository.Result result = repository.createOnce(idempotencyKey, () -> create(request));
        if (!result.created()) {
            Order existing = result.order();
            if (!existing.items().equals(request.items()) || !existing.currency().equals(request.currency())) {
                throw new IdempotencyConflictException();
            }
            log.atInfo().setMessage("idempotent replay").addKeyValue("order_id", existing.id()).log();
        }
        return result;
    }

    public Optional<Order> find(String id) {
        return repository.findById(id);
    }

    private Order create(CreateOrderRequest request) {
        String orderId = "ord_" + UUID.randomUUID().toString().replace("-", "");
        long totalCents = 0;
        for (OrderItem item : request.items()) {
            totalCents = Math.addExact(totalCents, Math.multiplyExact((long) item.quantity(), item.unitPriceCents()));
        }
        String paymentId = payments.charge(orderId, totalCents, request.currency(), request.cardNumber(), request.email());

        Order order = new Order(orderId, OrderStatus.CONFIRMED, List.copyOf(request.items()), totalCents,
                request.currency(), paymentId, Instant.now().toString());
        log.atInfo().setMessage("order confirmed")
                .addKeyValue("order_id", orderId)
                .addKeyValue("total_cents", totalCents)
                .addKeyValue("payment_id", paymentId)
                .log();
        return order;
    }
}