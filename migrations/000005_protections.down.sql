DROP TRIGGER IF EXISTS wallets_no_delete ON wallets;
DROP TRIGGER IF EXISTS wallets_update_rules ON wallets;
DROP FUNCTION IF EXISTS enforce_wallet_update_rules();

DROP TRIGGER IF EXISTS wager_transactions_no_delete ON wager_transactions;
DROP TRIGGER IF EXISTS wager_transactions_terminal_is_final ON wager_transactions;
DROP FUNCTION IF EXISTS reject_terminal_transition();

DROP TRIGGER IF EXISTS wallet_ledger_entries_no_truncate ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS wallet_ledger_entries_no_delete ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS wallet_ledger_entries_no_update ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS reject_ledger_mutation();
