-- Reverse 000018. Drop in reverse FK-dependency order: warmup_receipts and
-- warmup_daily_stats have no dependents; warmup_receipts and warmup_sends must
-- go before warmup_threads (they reference it); warmup_participants is
-- independent. Each DROP also removes its own indexes. up/down/up is clean.
DROP TABLE warmup_daily_stats;
DROP TABLE warmup_receipts;
DROP TABLE warmup_sends;
DROP TABLE warmup_threads;
DROP TABLE warmup_participants;
