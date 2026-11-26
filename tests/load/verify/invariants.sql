-- Post-run financial invariants, read-only.
--
-- Every statement below is a SELECT. This file never inserts, updates, or
-- deletes: PostgreSQL is the authority for what happened, and a verification
-- step that could change it would be worthless.
--
-- The result is one row per check: (check_name, violations). Zero violations is
-- the only pass. run-verify.sh turns that into an exit code.
--
-- psql variable: pending_age_seconds (default 60 in run-verify.sh).

WITH checks AS (
    -- 1. A wallet can never go below zero. This is also enforced in the
    --    transfer transaction, so a violation here means the constraint was
    --    bypassed, not that a client sent a strange amount.
    SELECT 'wallets with a negative balance' AS check_name,
           count(*)::bigint AS violations
      FROM wallets
     WHERE balance < 0

    UNION ALL

    -- 2. The wallet row is the hot copy of the balance; the ledger account is
    --    the record. They must agree for every wallet.
    SELECT 'wallet balance differs from its ledger account',
           count(*)::bigint
      FROM (
          SELECT w.id
            FROM wallets w
            JOIN ledger_accounts la
              ON la.wallet_id = w.id
             AND la.account_type = 'wallet'
            LEFT JOIN ledger_entries le ON le.account_id = la.id
           GROUP BY w.id, w.balance
          HAVING w.balance <> COALESCE(SUM(le.amount), 0)
      ) AS mismatched

    UNION ALL

    -- 3. Double entry: every transaction has exactly two entries.
    SELECT 'ledger transactions without exactly two entries',
           count(*)::bigint
      FROM (
          SELECT lt.id
            FROM ledger_transactions lt
            LEFT JOIN ledger_entries le ON le.transaction_id = lt.id
           GROUP BY lt.id
          HAVING count(le.id) <> 2
      ) AS wrong_entry_count

    UNION ALL

    -- 4. Double entry: those two entries sum to zero.
    SELECT 'ledger transactions that do not sum to zero',
           count(*)::bigint
      FROM (
          SELECT lt.id
            FROM ledger_transactions lt
            JOIN ledger_entries le ON le.transaction_id = lt.id
           GROUP BY lt.id
          HAVING SUM(le.amount) <> 0
      ) AS unbalanced

    UNION ALL

    -- 5. A transfer debits the source and credits the destination by the
    --    recorded amount, with the sign the ledger expects.
    SELECT 'transfers whose entries do not debit the source and credit the destination',
           count(*)::bigint
      FROM (
          SELECT lt.id
            FROM ledger_transactions lt
            JOIN ledger_entries le ON le.transaction_id = lt.id
            JOIN ledger_accounts la ON la.id = le.account_id
           WHERE lt.transaction_type = 'transfer'
           GROUP BY lt.id, lt.amount, lt.wallet_id, lt.destination_wallet_id
          HAVING COALESCE(SUM(le.amount) FILTER (WHERE la.wallet_id = lt.wallet_id), 0)
                     <> -lt.amount
              OR COALESCE(SUM(le.amount) FILTER (WHERE la.wallet_id = lt.destination_wallet_id), 0)
                     <> lt.amount
      ) AS wrong_direction

    UNION ALL

    -- 6. Money is neither created nor destroyed by a transfer. Per currency,
    --    the sum of wallet balances equals the sum of deposits, because
    --    deposits are the only way funds enter the system.
    SELECT 'funds not conserved for a currency',
           count(*)::bigint
      FROM (
          SELECT w.currency,
                 SUM(w.balance) AS wallet_total,
                 (SELECT COALESCE(SUM(lt.amount), 0)
                    FROM ledger_transactions lt
                   WHERE lt.transaction_type = 'deposit'
                     AND lt.currency = w.currency) AS deposited_total
            FROM wallets w
           GROUP BY w.currency
      ) AS per_currency
     WHERE wallet_total <> deposited_total

    UNION ALL

    -- 7. One idempotency key is one financial effect. The unique constraint
    --    should make this impossible; a violation means the constraint is gone.
    SELECT 'idempotency keys that produced more than one transaction',
           count(*)::bigint
      FROM (
          SELECT user_id, idempotency_key
            FROM ledger_transactions
           GROUP BY user_id, idempotency_key
          HAVING count(*) > 1
      ) AS duplicated

    UNION ALL

    -- 8. Every committed transfer has an outbox event waiting to be published.
    --    This is the transactional outbox guarantee: the event is written in the
    --    same transaction as the money movement.
    SELECT 'transfers without a matching outbox event',
           count(*)::bigint
      FROM ledger_transactions lt
     WHERE lt.transaction_type = 'transfer'
       AND NOT EXISTS (
           SELECT 1
             FROM outbox_events oe
            WHERE oe.aggregate_id = lt.id
              AND oe.event_type = 'transfer.completed'
       )

    UNION ALL

    -- 9. The worker keeps up: nothing stays unpublished past the threshold.
    SELECT 'outbox events unpublished after ' || :'pending_age_seconds' || 's',
           count(*)::bigint
      FROM outbox_events
     WHERE published_at IS NULL
       AND created_at < now() - (:'pending_age_seconds' || ' seconds')::interval

    UNION ALL

    -- 10. The consumer keeps up: published events reach processed_events.
    SELECT 'published events not consumed after ' || :'pending_age_seconds' || 's',
           count(*)::bigint
      FROM outbox_events oe
     WHERE oe.published_at IS NOT NULL
       AND oe.published_at < now() - (:'pending_age_seconds' || ' seconds')::interval
       AND NOT EXISTS (
           SELECT 1 FROM processed_events pe WHERE pe.event_id = oe.id
       )

    UNION ALL

    -- 11. A processed transfer produces one notification per side. Duplicate
    --     delivery must be absorbed by processed_events, not by the inbox.
    SELECT 'processed transfer events without exactly two notifications',
           count(*)::bigint
      FROM (
          SELECT pe.event_id
            FROM processed_events pe
            JOIN outbox_events oe ON oe.id = pe.event_id
            LEFT JOIN notifications n ON n.event_id = pe.event_id
           WHERE oe.event_type = 'transfer.completed'
           GROUP BY pe.event_id
          HAVING count(n.id) <> 2
      ) AS notification_count

    UNION ALL

    -- 12. Two notifications for the same event and user would double-count a
    --     transfer in the user's inbox.
    SELECT 'duplicate notifications for one event and user',
           count(*)::bigint
      FROM (
          SELECT event_id, user_id
            FROM notifications
           GROUP BY event_id, user_id
          HAVING count(*) > 1
      ) AS duplicate_notifications
)
SELECT check_name, violations
  FROM checks
 ORDER BY check_name;
