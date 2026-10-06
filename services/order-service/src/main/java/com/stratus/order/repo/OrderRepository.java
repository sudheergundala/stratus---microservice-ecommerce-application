package com.stratus.order.repo;

import com.stratus.order.domain.Order;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Supplier;
import org.springframework.stereotype.Repository;

/**
 * In-memory order storage keyed by idempotency key. Correct for a single
 * replica only; PostgreSQL replaces it.
 */
@Repository
public class OrderRepository {

    /** The order, and whether this call created it (false = replay). */
    public record Result(Order order, boolean created) {
    }

    private final ConcurrentHashMap<String, CompletableFuture<Order>> byKey = new ConcurrentHashMap<>();
    private final ConcurrentHashMap<String, Order> byId = new ConcurrentHashMap<>();

    /**
     * Runs create at most once per key. Concurrent requests with the same key
     * wait for the first one and share its result. Nothing is locked while
     * create runs, so unrelated orders never wait for each other.
     */
    public Result createOnce(String key, Supplier<Order> create) {
        CompletableFuture<Order> mine = new CompletableFuture<>();
        CompletableFuture<Order> existing = byKey.putIfAbsent(key, mine);
        if (existing != null) {
            try {
                return new Result(existing.join(), false);
            } catch (CompletionException e) {
                if (e.getCause() instanceof RuntimeException cause) {
                    throw cause;
                }
                throw e;
            }
        }
        try {
            Order order = create.get();
            byId.put(order.id(), order);
            mine.complete(order);
            return new Result(order, true);
        } catch (RuntimeException e) {
            byKey.remove(key, mine); // a failed attempt may be retried with the same key
            mine.completeExceptionally(e);
            throw e;
        }
    }

    public Optional<Order> findById(String id) {
        return Optional.ofNullable(byId.get(id));
    }
}