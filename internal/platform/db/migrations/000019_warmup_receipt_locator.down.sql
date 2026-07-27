-- Reverse 000019: drop the receipt locator columns.
ALTER TABLE warmup_receipts
    DROP COLUMN source_folder,
    DROP COLUMN message_id;
