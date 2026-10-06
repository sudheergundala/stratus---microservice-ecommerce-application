package com.stratus.order.api;

import com.stratus.order.domain.Order;
import com.stratus.order.repo.OrderRepository;
import com.stratus.order.service.OrderService;
import jakarta.validation.Valid;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestHeader;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ResponseStatusException;

@RestController
@RequestMapping("/orders")
public class OrderController {

    private final OrderService orders;

    public OrderController(OrderService orders) {
        this.orders = orders;
    }

    @PostMapping
    public ResponseEntity<Order> create(@RequestHeader("Idempotency-Key") String idempotencyKey,
                                        @Valid @RequestBody CreateOrderRequest request) {
        if (idempotencyKey.isBlank() || idempotencyKey.length() > 255) {
            throw new ResponseStatusException(HttpStatus.BAD_REQUEST, "Idempotency-Key must be 1-255 characters");
        }
        OrderRepository.Result result = orders.placeOrder(idempotencyKey, request);
        return ResponseEntity.status(result.created() ? HttpStatus.CREATED : HttpStatus.OK).body(result.order());
    }

    @GetMapping("/{id}")
    public Order get(@PathVariable String id) {
        return orders.find(id)
                .orElseThrow(() -> new ResponseStatusException(HttpStatus.NOT_FOUND, "order not found"));
    }
}