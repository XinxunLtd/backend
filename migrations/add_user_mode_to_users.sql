-- Add user_mode column to users table
ALTER TABLE users ADD COLUMN user_mode ENUM('real','promotor') DEFAULT 'real';

