module.exports = {
  preset: 'ts-jest',
  testEnvironment: 'node',
  roots: ['<rootDir>/packages'],
  testMatch: ['**/*.test.ts'],
  moduleNameMapper: {
    '^(\.{1,2}/.*)\.js$': '$1',
    '^@whitetransport/provider-channels$': '<rootDir>/../provider-channels/src/index.ts',
    '^@whitetransport/video-conference-transport$': '<rootDir>/../video-conference-transport/src/index.ts',
  },
  collectCoverageFrom: ['packages/**/*.ts', '!packages/**/*.test.ts'],
};
