-- Papel da aplicação.
--
-- Criado no bootstrap do banco porque a senha não pertence a uma migration
-- versionada. As migrations concedem os privilégios; aqui só existe o papel.
--
-- É este papel que recebe apenas SELECT e INSERT no ledger: a aplicação não
-- chega a ter o privilégio de alterar um lançamento.
CREATE ROLE jungle_app LOGIN PASSWORD 'jungle_app';
